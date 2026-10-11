"""Spike 28: what does a hosted session leave running when its host dies, and can baton find it again?

usage: python3 run.py MODE     MODE: host9 (SIGKILL the host), both9 (SIGKILL the host and its claude),
                                     hup (SIGHUP the host, as a closed terminal does)

A dev build of baton (../../dist/baton, `make build`) hosts claude --model haiku in work/proj, with BATON_HOME in
work/home. The model runs start.sh (a background `sleep 3601`, a python http.server in a stand-in worker
worktree under BATON_HOME, and a daemon that calls setsid and chdir / before `sleep 3603`), then a
run_in_background `sleep 3604`, then a foreground `sleep 3605`. While that last one runs, the host is
killed. Each process is then read with ../out/procs (sysctl and proc_info, no cgo), and with ps -E and lsof
to compare. Everything the spike started is stopped at the end.
"""
import json, os, pty, re, select, signal, subprocess, sys, threading, time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
BATON = os.path.join(REPO, 'dist', 'baton')
PROCS = os.path.join(HERE, 'out', 'procs')
WORK = os.path.join(HERE, 'work')
HOME = os.path.join(WORK, 'home')
PROJ = os.path.join(WORK, 'proj')
WT = os.path.join(HOME, 'worktrees', 'k', 'r', 'P1')
ANSI = re.compile(rb'\x1b\[[0-9;?<>=:]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78M]')
MARKS = {'sleep 3601': 'bg sleep', 'http.server': 'dev server in worktree', 'sleep 3603': 'setsid daemon',
         'sleep 3604': 'run_in_background task', 'sleep 3605': 'foreground tool call'}
PROMPT = ('Use the Bash tool for each of these, one call each, in order, exactly as written, without '
          'explaining: first `sh start.sh`; then `sleep 3604` with run_in_background set to true; then `sleep 3605`.')

START_SH = f'''sleep 3601 > /dev/null 2>&1 &
(cd "{WT}" && exec python3 -m http.server 0 --bind 127.0.0.1 > /dev/null 2>&1) &
perl -e 'use POSIX; setsid(); chdir "/"; exec "sleep", "3603"' > /dev/null 2>&1 &
echo started
'''


def sh(*args, cwd=None):
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True).stdout


def setup():
    subprocess.run(['rm', '-rf', WORK])
    os.makedirs(PROJ)
    sh('git', 'init', '-q', '-b', 'main', cwd=PROJ)
    open(os.path.join(PROJ, 'start.sh'), 'w').write(START_SH)
    sh('git', 'add', '.', cwd=PROJ)
    sh('git', '-c', 'user.name=s', '-c', 'user.email=s@x', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'start', cwd=PROJ)
    os.makedirs(os.path.dirname(WT))
    sh('git', 'worktree', 'add', '-q', '-b', 'spike/P1', WT, cwd=PROJ)


class Host:
    def __init__(self):
        e = os.environ
        env = {k: e[k] for k in ('HOME', 'USER', 'LOGNAME', 'TMPDIR', 'LANG', 'SHELL') if k in e}
        env.update(TERM='xterm-256color', PATH=os.path.dirname(BATON) + ':' + e['PATH'], BATON_HOME=HOME,
                   BATON_WORKERS='0', BATON_NOTIFY_DESKTOP='0', BATON_NTFY_TOPIC='')
        argv = [BATON, '--model', 'haiku', '--allowedTools', 'Bash']
        self.buf = b''
        self.lock = threading.Lock()
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(PROJ)
            os.execve(BATON, argv, env)
        self.pid, self.fd = pid, fd
        threading.Thread(target=self.pump, daemon=True).start()

    def pump(self):
        while True:
            try:
                r, _, _ = select.select([self.fd], [], [], 0.2)
                if r:
                    d = os.read(self.fd, 65536)
                    if not d:
                        return
                    with self.lock:
                        self.buf += d
            except OSError:
                return

    def screen(self):
        with self.lock:
            return ANSI.sub(b'', self.buf).decode('utf-8', 'replace').replace(' ', '')

    def wait(self, text, timeout):
        end = time.time() + timeout
        while time.time() < end:
            if text.replace(' ', '') in self.screen():
                return True
            time.sleep(0.25)
        return False

    def type(self, text):
        for ch in text:
            os.write(self.fd, ch.encode())
            time.sleep(0.02)
        time.sleep(0.5)
        os.write(self.fd, b'\r')


def children(pid):
    return [int(p) for p in sh('pgrep', '-P', str(pid)).split()]


def marked():
    """The spike's own processes, by command line."""
    found = {}
    for line in sh('ps', '-A', '-ww', '-o', 'pid=,command=').splitlines():
        pid, _, cmd = line.strip().partition(' ')
        for m, what in MARKS.items():
            if m in cmd and 'pgrep' not in cmd and ' -c ' not in cmd:
                found.setdefault(what, []).append(int(pid))
    return found


def read(pid):
    p = json.loads(sh(PROCS, 'show', str(pid)))
    env = p.get('baton_env') or {}
    p['baton_env'] = {k: env[k] for k in ('BATON_INSTANCE', 'BATON_DIR', 'BATON_ROLE') if k in env}
    ps_env = sh('ps', '-E', '-ww', '-o', 'command=', '-p', str(pid))
    p['ps_E_has_instance'] = 'BATON_INSTANCE=' in ps_env
    p['lsof_cwd'] = next((l[1:] for l in sh('lsof', '-a', '-p', str(pid), '-d', 'cwd', '-Fn').splitlines() if l.startswith('n')), None)
    return p


def snapshot(label, log):
    out = {'label': label, 'procs': {}}
    for what, pids in marked().items():
        out['procs'][what] = [read(p) for p in pids]
    log.write(json.dumps(out) + '\n')
    log.flush()
    print(f'--- {label}')
    for what in MARKS.values():
        ps = out['procs'].get(what, [])
        if not ps:
            print(f'  {what:24} gone')
        for p in ps:
            print(f"  {what:24} pid {p['pid']} {p.get('comm', '')} ppid {p.get('ppid')} pgid {p.get('pgid')} sid {p.get('sid')}"
                  f" cwd {p.get('cwd', '').replace(WORK, 'work')} instance {p['baton_env'].get('BATON_INSTANCE')}"
                  f" ps-E {p['ps_E_has_instance']} lsof {'=' if p['lsof_cwd'] == p.get('cwd') else p['lsof_cwd']}")
    return out


def stop_all():
    for _ in range(3):
        for pids in marked().values():
            for p in pids:
                try:
                    os.kill(p, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        time.sleep(0.5)


def main(mode):
    if marked():
        sys.exit(f'processes from an earlier run are still alive: {marked()}')
    setup()
    os.makedirs(os.path.join(HERE, 'out'), exist_ok=True)
    log = open(os.path.join(HERE, 'out', f'{mode}.jsonl'), 'w')
    h = Host()
    print(f'host pid {h.pid}')
    if h.wait('trust this folder', 20):
        time.sleep(0.8); os.write(h.fd, b'\x1b[B'); time.sleep(0.4); os.write(h.fd, b'\r')
    time.sleep(6)
    h.type(PROMPT)
    end = time.time() + 120
    while time.time() < end and 'foreground tool call' not in marked():
        time.sleep(1)
    time.sleep(2)
    claude = children(h.pid)
    print(f'claude {claude}; tool shells {[children(c) for c in claude]}')
    before = snapshot('before', log)
    if not before['procs'].get('foreground tool call'):
        print(h.screen()[-3000:])
        stop_all()
        sys.exit('the model did not get to the foreground sleep')
    t = time.time()
    if mode == 'host9':
        os.kill(h.pid, signal.SIGKILL)
    elif mode == 'both9':
        os.kill(h.pid, signal.SIGKILL)
        for c in claude:
            os.kill(c, signal.SIGKILL)
    elif mode == 'hup':
        os.kill(h.pid, signal.SIGHUP)
    os.waitpid(h.pid, 0)
    time.sleep(5)
    print(f'claude alive after {time.time() - t:.0f}s: {[c for c in claude if sh("ps", "-o", "pid=", "-p", str(c)).strip()]}')
    snapshot('after', log)
    stop_all()
    print('stopped:', marked() or 'nothing left')


if __name__ == '__main__':
    main(sys.argv[1])
