"""PTY driver for spike 23 (worker start). Based on spikes/21-attach/drv.py.

Every claude is started as `env -i HOME USER LOGNAME PATH TMPDIR LANG SHELL TERM [PWD] claude --model haiku
--settings <hooks> ...` so no BATON_*/CLAUDE_* variable leaks in. Hooks log every event's stdin JSON through
hook.sh into <log>. Raw output chunks are kept with timestamps; vt.py renders the screen.
"""
import os, pty, signal, struct, fcntl, termios, threading, time, json, re, select, subprocess, sys
import vt
HERE = os.path.dirname(os.path.abspath(__file__))
HOOK = os.path.join(HERE, 'hook.sh')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop', 'StopFailure',
          'Notification', 'PermissionRequest']
ANSI = re.compile(rb'\x1b\[[0-9;?<>=:]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78M]')


def settings(log, codeword=None, hook_sleep=None, extra=None):
    pre = ''
    if codeword: pre += f'CODEWORD={codeword} '
    if hook_sleep: pre += f'HOOK_SLEEP={hook_sleep} '
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'{pre}{HOOK} {ev} {log}', 'timeout': 30}]}] for ev in EVENTS}
    s = {'hooks': hooks}
    s.update(extra or {})
    return json.dumps(s)


def clean_env_argv(args, pwd=None):
    e = os.environ
    argv = ['env', '-i', f'HOME={e["HOME"]}', f'USER={e["USER"]}', f'LOGNAME={e.get("LOGNAME", e["USER"])}',
            f'PATH={e["PATH"]}', f'TMPDIR={e["TMPDIR"]}', f'LANG={e.get("LANG", "en_US.UTF-8")}',
            f'SHELL={e["SHELL"]}', 'TERM=xterm-256color']
    if pwd: argv.append(f'PWD={pwd}')
    return argv + ['claude', '--model', 'haiku'] + list(args)


class Drv:
    def __init__(self, cwd, args, log, name='w', codeword=None, hook_sleep=None, pwd=None, rows=50, cols=160,
                 extra_settings=None):
        self.cwd, self.rows, self.cols, self.name, self.log = cwd, rows, cols, name, log
        self.chunks = []; self.marks = []; self.keys_sent = 0
        self.lock = threading.Lock(); self.closed = False
        self.argv = clean_env_argv(['--settings', settings(log, codeword, hook_sleep, extra_settings)] + list(args), pwd)
        self.t0 = time.time(); self.last_out = self.t0
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            os.execvp(self.argv[0], self.argv)
        self.pid, self.fd = pid, fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        self.alive = True; self.status = None; self.t_exit = None
        threading.Thread(target=self._pump, daemon=True).start()
        os.makedirs(os.path.join(HERE, 'out'), exist_ok=True)
        open(os.path.join(HERE, 'out', 'pids.txt'), 'a').write(f'{pid} {name}\n')
        self.mark(f'launched pid {pid} in {short(cwd)} args {[short(a) for a in args]}')

    def _pump(self):
        while not self.closed:
            try:
                r, _, _ = select.select([self.fd], [], [], 0.2)
                if not r: continue
                data = os.read(self.fd, 65536)
            except (OSError, ValueError):
                break
            if not data: break
            with self.lock:
                self.chunks.append((time.time(), data)); self.last_out = time.time()
        self.alive = False

    def mark(self, label):
        with self.lock: self.marks.append((time.time(), label))
        print(f'[{self.name} {time.time()-self.t0:6.1f}s] {label}', flush=True)

    # --- screen
    def plain(self):
        with self.lock: b = b''.join(c for _, c in self.chunks)
        return ANSI.sub(b'', b)
    def has(self, needle):
        return needle.encode().replace(b' ', b'') in self.plain()[-40000:].replace(b' ', b'')
    def screen(self):
        s = vt.Screen(self.rows, self.cols)
        with self.lock:
            for _, c in self.chunks: s.feed(c)
        return s.text()
    def dump(self, label):
        p = os.path.join(HERE, 'out', f'screen-{self.name}-{label}.txt')
        os.makedirs(os.path.dirname(p), exist_ok=True)
        txt = '\n'.join(l for l in self.screen().split('\n'))
        open(p, 'w').write(txt.rstrip('\n') + '\n')
        return txt

    # --- hooks
    def recs(self):
        try: return [json.loads(l) for l in open(self.log) if l.strip()]
        except FileNotFoundError: return []
    def count(self, ev): return sum(1 for r in self.recs() if r['ev'] == ev)
    def first(self, ev):
        return next((r for r in self.recs() if r['ev'] == ev), None)

    # --- waiting
    def wait(self, cond, timeout, what):
        end = time.time() + timeout
        while time.time() < end:
            if cond(): return True
            if not self.alive and not self.closed:
                self.mark(f'child exited waiting for {what}'); return cond()
            time.sleep(0.1)
        self.mark(f'TIMEOUT waiting for {what}'); return False
    def wait_has(self, needle, timeout=60): return self.wait(lambda: self.has(needle), timeout, repr(needle))
    def wait_quiet(self, secs=2.0, timeout=60): return self.wait(lambda: time.time()-self.last_out >= secs, timeout, f'{secs}s quiet')
    def wait_ev(self, ev, n=0, timeout=120): return self.wait(lambda: self.count(ev) > n, timeout, f'{ev} #{n+1}')

    # --- input
    def type(self, text, delay=0.01):
        for ch in text: os.write(self.fd, ch.encode()); self.keys_sent += 1; time.sleep(delay)
    def key(self, raw): os.write(self.fd, raw); self.keys_sent += 1
    def enter(self): time.sleep(0.4); self.key(b'\r')

    def trust_visible(self): return self.has('trust this folder') or self.has('Do you trust')
    def accept_trust(self):
        """Pick the option whose label starts with 'Yes' by moving the ❯ pointer to it (never the default)."""
        txt = self.screen(); lines = txt.split('\n')
        opts = []
        for l in lines:
            m = re.match(r'^\s*(❯)?\s*(\d\.\s+)?((?:Yes|No)\b.*\S)', l)
            if m: opts.append((bool(m.group(1)), (m.group(2) or '').strip(), m.group(3)))
        cur = next((i for i, o in enumerate(opts) if o[0]), 0)
        yes = next((i for i, o in enumerate(opts) if o[2].lower().startswith('yes')), None)
        self.mark(f'trust options {opts} pointer={cur} yes={yes}')
        if yes is None: raise RuntimeError('no Yes option on trust dialog')
        k = b'\x1b[B' if yes > cur else b'\x1b[A'
        for _ in range(abs(yes - cur)): time.sleep(0.3); self.key(k)
        time.sleep(0.5); self.key(b'\r'); self.mark('trust accepted (Yes)')
        return opts

    # --- processes
    def tree(self):
        out = subprocess.run(['ps', '-A', '-o', 'pid=,ppid=,pgid=,stat=,command='], capture_output=True, text=True).stdout
        rows = [l.split(None, 4) for l in out.split('\n') if l.strip()]
        kids = {self.pid}; changed = True
        while changed:
            changed = False
            for r in rows:
                if int(r[1]) in kids and int(r[0]) not in kids: kids.add(int(r[0])); changed = True
        return [(int(r[0]), int(r[1]), int(r[2]), r[3], r[4][:120]) for r in rows if int(r[0]) in kids]
    def pgid_members(self):
        out = subprocess.run(['ps', '-A', '-o', 'pid=,pgid=,stat=,command='], capture_output=True, text=True).stdout
        return [l.strip()[:140] for l in out.split('\n') if l.split() and l.split()[1] == str(self.pid)]
    def reap(self, timeout=15):
        end = time.time() + timeout
        while time.time() < end:
            p, st = os.waitpid(self.pid, os.WNOHANG)
            if p:
                self.status = st; self.t_exit = time.time()
                desc = f'exit code {os.waitstatus_to_exitcode(st)}' if hasattr(os, 'waitstatus_to_exitcode') else f'status {st}'
                if os.WIFSIGNALED(st): desc = f'killed by signal {os.WTERMSIG(st)}'
                elif os.WIFEXITED(st): desc = f'exit code {os.WEXITSTATUS(st)}'
                self.mark(f'reaped: {desc}'); return desc
            time.sleep(0.05)
        self.mark('still running after reap timeout'); return None

    # --- ending
    def end_exit(self):
        self.type('/exit'); self.enter(); self.mark('typed /exit + Enter')
    def end_sigterm(self):
        os.kill(self.pid, signal.SIGTERM); self.mark('sent SIGTERM')
    def end_close_master(self):
        self.closed = True; time.sleep(0.3); os.close(self.fd); self.mark('closed PTY master')
    def kill(self):
        for s in (signal.SIGTERM, signal.SIGKILL):
            try: os.kill(self.pid, s)
            except ProcessLookupError: return
            time.sleep(1.0)
        try: os.waitpid(self.pid, os.WNOHANG)
        except ChildProcessError: pass

    def save(self, label):
        p = os.path.join(HERE, 'out', f'{self.name}-{label}.json')
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with self.lock:
            json.dump({'argv': self.argv, 'marks': self.marks, 'chunks': [(t, c.decode('latin-1')) for t, c in self.chunks]}, open(p, 'w'))


def git(*a, cwd=None):
    r = subprocess.run(['git', *a], cwd=cwd, capture_output=True, text=True)
    return (r.stdout + r.stderr).strip()


def rel(t, t0): return None if t is None else round(t - t0, 2)


PROJ_ESC = re.sub(r'[^A-Za-z0-9]', '-', HERE)
def short(v):
    if not isinstance(v, str): return v
    return v.replace(os.path.expanduser('~/.claude/projects/') + PROJ_ESC, '~/.claude/projects/<esc $S>').replace(HERE, '$S')


def brief(r, t0):
    i = r.get('input') or {}
    keep = {}
    for k in ('source', 'session_id', 'cwd', 'transcript_path', 'reason', 'prompt', 'last_assistant_message', 'tool_name',
              'permission_mode', 'model'):
        if k in i:
            v = i[k]
            if isinstance(v, str) and len(v) > 160 and k != 'transcript_path': v = v[:160] + '…'
            keep[k] = short(v)
    if r['ev'] in ('PreToolUse', 'PostToolUse') and 'tool_input' in i:
        keep['tool_input'] = str(i['tool_input'])[:120]
    if r['ev'] == 'PostToolUse' and 'tool_response' in i:
        keep['tool_response'] = str(i['tool_response'])[:300]
    if r.get('pwd'): keep['hook_PWD'] = short(r['pwd'])
    if r.get('cpd'): keep['CLAUDE_PROJECT_DIR'] = short(r['cpd'])
    return (rel(r['t'], t0), r['ev'], keep)


def report(d, title):
    print(f'==== {title}: hooks (seconds after launch)')
    for t, ev, keep in (brief(r, d.t0) for r in d.recs()):
        print(f'  {t:7.2f} {ev:18} {json.dumps(keep)}')
    sys.stdout.flush()


def last_answer(d):
    st = [r for r in d.recs() if r['ev'] == 'Stop']
    if st and (st[-1].get('input') or {}).get('last_assistant_message'):
        return st[-1]['input']['last_assistant_message']
    tp = next(((r.get('input') or {}).get('transcript_path') for r in d.recs() if (r.get('input') or {}).get('transcript_path')), None)
    try:
        msgs = [json.loads(l) for l in open(tp)]
        for m in reversed(msgs):
            if m.get('type') == 'assistant':
                c = m['message']['content']
                txt = ' '.join(x.get('text', '') for x in c if x.get('type') == 'text')
                if txt: return txt
    except Exception as e:
        return f'(no answer: {e})'
