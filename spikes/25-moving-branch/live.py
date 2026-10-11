#!/usr/bin/env python3
"""Q2/Q4 — the run branch fast-forwards under a live Haiku session (Claude Code 2.1.296).
usage: live.py <scenario>   scenarios: 2a 2b 2c 2d 4a 4b

The repo always lives at work/live/main (one fixed path, so the folder is trusted once), recreated
for each scenario; the worker worktree is work/live/main/.baton/worktrees/w on branch w. The host
lands a worker commit with: commit in the worktree, `git rebase run` there, then
`git merge --ff-only w` in the main checkout. Everything is saved to results/<scenario>/."""
import json, os, re, shutil, subprocess, sys, threading, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from scenario import Driver

LIVE = os.path.join(HERE, 'work', 'live'); MAIN = os.path.join(LIVE, 'main')
WT = os.path.join(MAIN, '.baton', 'worktrees', 'w')
G = ['git', '-c', 'user.name=spike', '-c', 'user.email=spike@example.invalid']
EVENTS = ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure', 'Stop',
          'Notification', 'PermissionRequest']
ALLOWED = ('Read,Edit,Write,Bash(sleep:*),Bash(git status:*),Bash(git log:*),Bash(git diff:*),Bash(git show:*),'
           'Bash(git commit:*),Bash(git add:*),Bash(git rev-parse:*),Bash(git reflog:*),Bash(git -C:*)')
CONFIG = '# zebra service config\nname = zebra\nport = 8080\nworkers = 4\nlog = info\ncolour = red\ntimeout = 30\n'
E = os.environ
ENV = ['env', '-i', f'HOME={E["HOME"]}', f'USER={E["USER"]}', f'LOGNAME={E.get("LOGNAME", E["USER"])}',
       f'PATH={E["PATH"]}', f'TMPDIR={E["TMPDIR"]}', f'LANG={E.get("LANG", "en_US.UTF-8")}', f'SHELL={E["SHELL"]}',
       'TERM=xterm-256color']


def git(*a, cwd=MAIN, check=True):
    r = subprocess.run(G + list(a), cwd=cwd, capture_output=True, text=True)
    if check and r.returncode:
        raise RuntimeError(f'git {a}: {r.stderr}')
    return r


class Scn:
    def __init__(self, name):
        self.name = name
        self.res = os.path.join(HERE, 'results', name)
        shutil.rmtree(self.res, ignore_errors=True); os.makedirs(self.res)
        self.log = os.path.join(self.res, 'hooks.log')
        self.announce = os.path.join(self.res, 'announce.txt')
        self.hostlog = open(os.path.join(self.res, 'host.log'), 'w')
        self.t0 = time.time()

    def h(self, msg):
        line = f'[{time.time() - self.t0:6.1f}s {time.strftime("%H:%M:%S")}] {msg}'
        print(line, flush=True); self.hostlog.write(line + '\n'); self.hostlog.flush()

    def gitlog(self, label, *a, cwd=MAIN):
        r = git(*a, cwd=cwd, check=False)
        self.h(f'{label}: $ git {" ".join(a)}  [exit {r.returncode}]\n' + (r.stdout + r.stderr).rstrip())
        return r

    # ---- repo
    def setup(self):
        shutil.rmtree(LIVE, ignore_errors=True); os.makedirs(MAIN)
        git('init', '-q', '-b', 'run')
        git('config', 'user.name', 'lead-spike'); git('config', 'user.email', 'lead-spike@example.invalid')
        open(os.path.join(MAIN, 'config.txt'), 'w').write(CONFIG)
        open(os.path.join(MAIN, 'notes.txt'), 'w').write('# notes\n')
        with open(os.path.join(MAIN, '.git', 'info', 'exclude'), 'a') as f:
            f.write('.baton/\nscreen.log\n')
        git('add', '.'); git('commit', '-qm', 'init')
        git('worktree', 'add', '-q', '-b', 'w', WT, 'run')

    def land(self, edit, msg):
        """The merge queue: worker commits, rebases onto run, host fast-forwards run in the main checkout."""
        t = time.time()
        edit(WT)
        git('add', '-A', cwd=WT); git('commit', '-qm', msg, cwd=WT)
        self.gitlog('worker rebase', 'rebase', 'run', cwd=WT)
        anc = git('merge-base', '--is-ancestor', 'run', 'w', check=False).returncode == 0
        landing = git('diff', '--name-only', 'HEAD', 'w').stdout.split()
        local = set(git('--no-optional-locks', 'diff', '--name-only', 'HEAD').stdout.split()) | \
            set(git('ls-files', '-o').stdout.split())
        self.h(f'probe: ff-possible={anc} landing={landing} overlap-with-local={sorted(set(landing) & local)}')
        r = self.gitlog('host land', 'merge', '--ff-only', 'w')
        st = os.stat(os.path.join(MAIN, 'config.txt'))
        self.h(f'landed in {time.time() - t:.3f}s; config.txt mtime={st.st_mtime:.3f}; now:\n'
               + open(os.path.join(MAIN, 'config.txt')).read().rstrip())
        self.landed_at = time.time()
        return r.returncode == 0

    # ---- session
    def start(self):
        hooks = {ev: [{'hooks': [{'type': 'command', 'command':
                                  f'python3 {HERE}/hooklog.py {ev} --log {self.log} --announce {self.announce}'}]}]
                 for ev in EVENTS}
        env = [e if not e.startswith('PATH=') else f'PATH={HERE}/shim:{E["PATH"]}' for e in ENV]
        env.append(f'GITSHIM_LOG={self.res}/gitshim.log')
        argv = env + ['claude', '--model', 'haiku', '--permission-mode', 'acceptEdits', '--allowedTools', ALLOWED,
                      '--settings', json.dumps({'hooks': hooks})]
        self.d = d = Driver(MAIN, argv)
        self.h(f'claude pid {d.pid}')
        d.wait(lambda: d.screen_has('trustthisfolder') or d.screen_has('acceptedits'), 40, 'trust prompt or idle')
        if d.screen_has('trustthisfolder'):
            with d.lock:
                open(os.path.join(self.res, 'screen-trust.txt'), 'w').write(d.tail[-3000:].decode('utf-8', 'replace'))
            time.sleep(0.8); os.write(d.fd, b'\x1b[B'); time.sleep(0.4); os.write(d.fd, b'\r')
            self.h('answered trust prompt (Down, Enter)')
        d.wait(lambda: self.count('SessionStart') > 0, 30, 'SessionStart')
        d.wait_quiet(3, 40)
        self.stop_sampling = False
        threading.Thread(target=self.sample_git, daemon=True).start()
        threading.Thread(target=self.guard, daemon=True).start()

    def sample_git(self):
        """Record every git process Claude Code itself spawns (descendants of the claude pid)."""
        seen = set()
        with open(os.path.join(self.res, 'gitprocs.log'), 'w') as f:
            while not self.stop_sampling and self.d.alive:
                out = subprocess.run(['ps', '-axo', 'pid=,ppid=,command='], capture_output=True, text=True).stdout
                kids, cmd = {}, {}
                for l in out.splitlines():
                    p = l.split(None, 2)
                    if len(p) < 3: continue
                    kids.setdefault(int(p[1]), []).append(int(p[0])); cmd[int(p[0])] = p[2]
                stack, desc = [self.d.pid], []
                while stack:
                    for k in kids.get(stack.pop(), []):
                        desc.append(k); stack.append(k)
                for pid in desc:
                    c = cmd.get(pid, '')
                    if re.search(r'(^|/| )git( |$)', c) and (pid, c) not in seen:
                        seen.add((pid, c)); f.write(f'{time.time() - self.t0:7.2f}s {pid} {c[:300]}\n'); f.flush()
                time.sleep(0.1)

    def guard(self):
        """Never let a permission dialog hang the run: reject it with Esc and note it."""
        while self.d.alive and not self.stop_sampling:
            if self.d.screen_has('Doyouwanttoproceed') or self.d.screen_has('Doyouwanttomake'):
                self.h('PERMISSION DIALOG on screen -> Esc'); self.dump('permission'); self.interrupted = True
                os.write(self.d.fd, b'\x1b'); time.sleep(3)
                with self.d.lock: self.d.tail = b''
            time.sleep(0.5)

    def recs(self):
        try:
            return [json.loads(l) for l in open(self.log) if l.strip()]
        except FileNotFoundError:
            return []

    def count(self, ev, pred=lambda r: True):
        return sum(1 for r in self.recs() if r['ev'] == ev and pred(r))

    def prompt(self, text):
        n = self.count('Stop')
        self.h(f'PROMPT: {text}')
        self.d.type(text, delay=0.01); self.d.enter()
        return n

    def wait_stop(self, n, timeout=180):
        self.interrupted = False
        ok = self.d.wait(lambda: self.count('Stop') > n or self.interrupted, timeout, 'Stop')
        self.d.wait_quiet(2, 20)
        last = [r for r in self.recs() if r['ev'] == 'Stop'][-1:]
        if last:
            self.h('Stop; last_assistant_message: ' + str(last[0]['input'].get('last_assistant_message', ''))[:1500])
        return ok

    def dump(self, label):
        with self.d.lock:
            t = self.d.tail[-6000:].decode('utf-8', 'replace')
        open(os.path.join(self.res, f'screen-{label}.txt'), 'w').write(t)

    def finish(self):
        self.dump('final')
        self.gitlog('final status', 'status', '--short')
        self.gitlog('final log', 'log', '--oneline', '--stat', '-5')
        self.gitlog('final diff', 'diff')
        self.h('final config.txt:\n' + open(os.path.join(MAIN, 'config.txt')).read().rstrip())
        self.stop_sampling = True
        self.d.type('/exit'); self.d.enter(); time.sleep(4); self.d.close()
        try:
            os.waitpid(self.d.pid, os.WNOHANG)
        except ChildProcessError:
            pass
        alive = subprocess.run(['ps', '-p', str(self.d.pid)], capture_output=True).returncode == 0
        self.h(f'claude {self.d.pid} still alive after close: {alive}')
        shutil.copy(os.path.join(MAIN, 'screen.log'), os.path.join(self.res, 'screen.log'))
        self.tools()
        self.transcript()

    def tools(self):
        """Tool calls as the hooks saw them."""
        with open(os.path.join(self.res, 'tools.txt'), 'w') as f:
            for r in self.recs():
                i = r['input']; ev = r['ev']
                if ev in ('PreToolUse', 'PostToolUse', 'PostToolUseFailure'):
                    f.write(f"{r['hms']} {float(r['t']) - self.t0:7.1f}s {ev} {i.get('tool_name')} "
                            f"input={json.dumps(i.get('tool_input'))[:600]}\n")
                    if ev == 'PostToolUse':
                        f.write(f"    tool_response={json.dumps(i.get('tool_response'))[:2500]}\n")
                    if ev == 'PostToolUseFailure':
                        f.write(f"    error={json.dumps(i.get('error'))[:1500]}\n")
                elif ev in ('UserPromptSubmit', 'Stop'):
                    f.write(f"{r['hms']} {float(r['t']) - self.t0:7.1f}s {ev}"
                            f"{' EMITTED ' + json.dumps(r['emitted']) if r.get('emitted') else ''}\n")

    def transcript(self):
        tp = next((r['input'].get('transcript_path') for r in self.recs() if r['input'].get('transcript_path')), None)
        if not tp or not os.path.exists(tp):
            self.h(f'no transcript ({tp})'); return
        shutil.copy(tp, os.path.join(self.res, 'transcript.jsonl'))
        # every place the model was told something about a file changing, plus all tool results
        with open(os.path.join(self.res, 'transcript-excerpt.txt'), 'w') as f:
            for l in open(tp):
                try:
                    e = json.loads(l)
                except Exception:
                    continue
                s = json.dumps(e)
                typ = e.get('type')
                if typ == 'attachment' or 'modified' in s or 'tool_result' in s or 'system-reminder' in s:
                    f.write(f"--- type={typ} ts={e.get('timestamp')}\n")
                    f.write(json.dumps(e.get('attachment') or e.get('message') or e, indent=1)[:4000] + '\n')


def patch(old, new, append=''):  # the worker's edit, applied to whatever its worktree has
    def f(wt):
        p = os.path.join(wt, 'config.txt'); t = open(p).read()
        open(p, 'w').write(t.replace(old, new) + append)
    return f
PORT = patch('port = 8080', 'port = 9090', 'cache = on\n')
COLOUR = patch('colour = red', 'colour = green')

MIDTURN = ('Do these steps in order. 1) Read config.txt with the Read tool. 2) Run the Bash command `sleep 20`. '
           '3) Use the Edit tool to change the line `colour = red` to `colour = blue` in config.txt, without reading the '
           'file again first. 4) Without running any more tools, tell me the value of port in config.txt as you '
           'understand it, and whether config.txt changed since you first read it.')


def s2a(s):  # idle landing, then Edit without re-reading
    n = s.prompt('Use the Read tool to read config.txt, then reply with only the value of colour. Do not edit anything.')
    s.wait_stop(n)
    s.land(PORT, 'P3: move to port 9090, enable cache')
    time.sleep(3)
    n = s.prompt('Use the Edit tool to change the line `colour = red` to `colour = blue` in config.txt. Do not read the '
                 'file again before editing. Then, without running any tools, tell me the value of port in config.txt as '
                 'you understand it, and whether config.txt changed since you read it.')
    s.wait_stop(n)


def midturn(s, edit, msg):
    n = s.prompt(MIDTURN)
    s.d.wait(lambda: s.count('PreToolUse', lambda r: 'sleep' in json.dumps(r['input'].get('tool_input'))) > 0, 90,
             'PreToolUse(Bash sleep)')
    time.sleep(3)
    s.h('sleep is running; landing now')
    s.land(edit, msg)
    s.wait_stop(n, 240)


def s2b(s): midturn(s, PORT, 'P3: move to port 9090, enable cache')
def s2c(s): midturn(s, COLOUR, 'P3: colour green')


def s2d(s):  # idle landing, then Write the whole file from memory
    n = s.prompt('Use the Read tool to read config.txt, then reply with only the value of colour. Do not edit anything.')
    s.wait_stop(n)
    s.land(PORT, 'P3: move to port 9090, enable cache')
    time.sleep(3)
    n = s.prompt('Use the Write tool (not Edit) to rewrite config.txt in full: exactly the content you read earlier, '
                 'except that colour is blue. Do not read the file again before writing.')
    s.wait_stop(n)


NATURAL = ('Do these steps in order. 1) Read config.txt with the Read tool. 2) Run the Bash command `sleep 20`. '
           '3) Use the Edit tool to change the colour setting in config.txt from red to blue. '
           '4) Tell me the final colour and port lines of config.txt.')


def natural(s, announce):  # same-line landing mid-turn; the model is free to re-read
    n = s.prompt(NATURAL)
    s.d.wait(lambda: s.count('PreToolUse', lambda r: 'sleep' in json.dumps(r['input'].get('tool_input'))) > 0, 90,
             'PreToolUse(Bash sleep)')
    time.sleep(3)
    s.h('sleep is running; landing now')
    s.land(COLOUR, 'P3: colour green')
    if announce:
        short = git('rev-parse', '--short', 'HEAD').stdout.strip()
        open(s.announce, 'w').write(
            f'baton: work from another phase just landed on the branch you are on (run), underneath you. New commit '
            f'{short} "P3: colour green" (not yours) changed: config.txt. Your uncommitted changes were not touched. '
            'Re-read any of those files before editing them, and never amend, reset, rebase or revert commits you did '
            'not make; if a task needs that, stop and say so.')
        s.h('announcement queued for the next PostToolUse')
    s.wait_stop(n, 240)


def s2h(s):  # the lead EDITED and committed config.txt in this turn; a landing changes it during a sleep; edit again
    n = s.prompt("Do these steps in order. 1) Use the Edit tool to change `colour = red` to `colour = blue` in config.txt. "
                 "2) Commit it with the Bash command: git commit -am 'lead: blue'. 3) Run the Bash command `sleep 20`. "
                 "4) Use the Edit tool to change `timeout = 30` to `timeout = 60` in config.txt. 5) Without running any "
                 "more tools, tell me the value of port in config.txt as you understand it, and whether config.txt "
                 "changed since you last saw it.")
    s.d.wait(lambda: s.count('PreToolUse', lambda r: 'sleep' in json.dumps(r['input'].get('tool_input'))) > 0, 120,
             'PreToolUse(Bash sleep)')
    time.sleep(3)
    s.h('sleep is running; landing now')
    s.land(PORT, 'P3: move to port 9090, enable cache')
    s.wait_stop(n, 240)


def s2e(s):  # the lead EDITED and committed config.txt; then a landing changes it; next prompt edits again
    n = s.prompt("Use the Edit tool to change `colour = red` to `colour = blue` in config.txt, then commit it with the Bash "
                 "command: git commit -am 'lead: blue'. Reply 'done'.")
    s.wait_stop(n)
    s.land(PORT, 'P3: move to port 9090, enable cache')
    time.sleep(3)
    n = s.prompt('Use the Edit tool to change `timeout = 30` to `timeout = 60` in config.txt, without reading the file again '
                 'first. Then, without running any tools, tell me the value of port in config.txt as you understand it, '
                 'and whether config.txt changed since you last saw it.')
    s.wait_stop(n)


ANNOUNCE = ('baton: work from another phase just landed on the branch you are on (run), underneath you. '
            'New commit {short} "P3: move to port 9090, enable cache" (not yours) changed: config.txt. '
            'Your uncommitted changes were not touched. Your own last commit is now {mine}, which is no longer HEAD. '
            'Re-read any of those files before editing them, and never amend, reset, rebase or revert commits '
            'you did not make; if a task needs that, stop and say so.')


def s4(s, announce):
    n = s.prompt("Append the line `lead was here` to notes.txt with the Edit tool, then commit it with the Bash command: "
                 "git commit -am 'lead: note'. Reply 'done'.")
    s.wait_stop(n)
    mine = git('rev-parse', '--short', 'HEAD').stdout.strip()
    s.h(f"lead's commit: {mine} {git('log', '-1', '--format=%s').stdout.strip()}")
    s.land(PORT, 'P3: move to port 9090, enable cache')
    if announce:
        short = git('rev-parse', '--short', 'HEAD').stdout.strip()
        open(s.announce, 'w').write(ANNOUNCE.format(short=short, mine=mine))
        s.h('announcement queued for the next UserPromptSubmit')
    time.sleep(3)
    n = s.prompt("Change the message of the commit you just made to 'lead: add note line', using git commit --amend -m. "
                 "Then run git log --oneline -4 and git status, and tell me which commits and changes in this repo are "
                 "yours and which are not.")
    s.wait_stop(n)
    s.gitlog('was P3 amended?', 'log', '--format=%h %s', '-4')


if __name__ == '__main__':
    name = sys.argv[1]
    s = Scn(name); s.setup(); s.start()
    try:
        {'2a': s2a, '2b': s2b, '2c': s2c, '2d': s2d, '2e': s2e, '2h': s2h, '2f': lambda s: natural(s, False), '2g': lambda s: natural(s, True), '4a': lambda s: s4(s, False), '4b': lambda s: s4(s, True)}[name](s)
    finally:
        s.finish()
