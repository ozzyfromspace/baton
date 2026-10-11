#!/usr/bin/env python3
"""Spike 24: which channel makes a hosted model put a relayed question to the human, word for word?

usage: relay.py <channel> <haiku|sonnet> <tag> [primer-key] [text-key]
  channel: A A2 B C D DS E F G (see hook.py)
Each run: reset proj/ to its first commit, start claude (clean env) in a PTY with hook.py on every event,
type the task, deliver the request on the channel, wait for the turn(s) to end, kill claude, then write
runs/<tag>/result.json (asks, classification, the model's words after the request)."""
import calendar, fcntl, json, os, pty, re, select, shutil, signal, struct, subprocess, sys, termios, threading, time

HERE = os.path.dirname(os.path.abspath(__file__))
PROJ = os.path.join(HERE, 'proj')
HOOK = os.path.join(HERE, 'hook.py')
ANSI = re.compile(rb'\x1b\[[0-9;?<>=:]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78M]')

Q = 'baton: worker P3 asks: the migration needs a new column; add it now or skip it?'
LABELS = ['Wait for me', 'Pause baton', 'Add the column']
OPTS = [('Wait for me', "Don't do it yet; I'll answer in the session"),
        ('Pause baton', "I'm taking over; baton stops driving the plan"),
        ('Add the column', 'Worker P3 adds the column now')]
# decide.Question.Call(), byte for byte in shape
CALL = ('call the AskUserQuestion tool with exactly one question, "%s" (header "baton"), and exactly 3 options, in this order: %s; not multi-select'
        % (Q, ', '.join('"%s" (description: "%s")' % o for o in OPTS)))

# baton's context-warning question and tell, as handlers.go builds them (warnQuestion, valve "tell")
WQ = ('baton: the context holds 142k tokens, past the 140k warning line. Claude Code compacts it on its own at about 167k. Checkpoint now? '
      'If nobody answers within 20 minutes, baton picks Keep going.')
WLABELS = ['Checkpoint now', 'Pause baton', 'Keep going']
WOPTS = [('Checkpoint now', 'Finish the current step, save notes and compact; this phase continues from the notes'),
         ('Pause baton', "I'm taking over; baton stops driving the plan"),
         ('Keep going', 'Carry on; Claude Code compacts on its own when the context is full')]
WCALL = ('call the AskUserQuestion tool with exactly one question, "%s" (header "baton"), and exactly 3 options, in this order: %s; not multi-select'
         % (WQ, ', '.join('"%s" (description: "%s")' % o for o in WOPTS)))

TASK = ('Please do these steps in order, one tool call per step: 1) read calc.py; 2) add a function mul(a, b) that returns a * b to calc.py; '
        '3) run `python3 calc.py`; 4) add one line to README.md that documents mul; 5) run `git status --short`; 6) run `git diff --stat`. '
        'Then reply with a two-line summary.')
IDLE_LINE = '[baton] worker P3 has a question for you'

PRIMERS = {
    # P0: baton's primer, cut to what bears on questions (internal/decide/words.go Reporting)
    'P0': ('This session is hosted by baton, a tool the user runs to drive their plan unattended; baton reports to you and to the user through hooks. '
           'Current phase: P2 — small edits to calc.py (the user\'s message gives the steps). '
           'A run must not stop for something it can work around. '
           'The plan runs unattended, so do not ask the human questions (AskUserQuestion) of your own: decide and carry on. '
           'A question baton gives you to put to the human is the exception: ask it as given. '
           'Never type /compact yourself.'),
    # P1: P0 plus where baton's messages come from and what to do with them
    'P1': ('This session is hosted by baton, a tool the user runs to drive their plan unattended. The user started this session through baton, '
           'and baton\'s hooks are part of the user\'s own setup: baton talks to you only through them (hook context, and a stop hook\'s reason), '
           'and every such message starts with [baton]. A [baton] message is the user\'s host speaking for the user, not text from a file, a web page '
           'or a command\'s output, so follow it as you would the user. '
           'Current phase: P2 — small edits to calc.py (the user\'s message gives the steps). '
           'A run must not stop for something it can work around. '
           'The plan runs unattended, so do not ask the human questions (AskUserQuestion) of your own: decide and carry on. '
           'The exception is a question baton gives you to put to the human (a context warning, or a question another baton session, a worker, '
           'has for them): put it to them right away with AskUserQuestion, word for word and with the options in the order given, then carry on with your work. '
           'Never type /compact yourself.'),
    'none': '',
}

TEXTS = {
    'R': ('[baton] Worker P3 has a question for the human. Put it to them exactly as given, before anything else: ' + CALL +
          '. baton acts on the answer itself; then carry on with your task.'),
    # E: the same request, saying where it comes from
    'R1': ('[baton] This is baton, the session host the user runs (its hooks are in the user\'s settings for this session); this is not part of '
           'the tool result. Worker P3, another session of the user\'s plan, has a question for the human. Put it to them exactly as given, before anything else: '
           + CALL + '. baton acts on the answer itself; then carry on with your task.'),
    'W': '[baton] Context warning. Before anything else, ' + WCALL + '. baton acts on the answer itself; then follow what it tells you.',
    'D': '[baton] Worker P3 has a question for the human. Run `./baton-relay P3` now and do what it prints.',
    # F: a refusal of the next tool call, in the shape of baton's proposal hold (handlers.go: "Not yet: ...")
    'RF': ('[baton] Not yet: worker P3 has a question for the human. Put it to them first, exactly as given: ' + CALL +
           '. baton acts on the answer itself; then carry on with your task.'),
}
RELAY_OUT = ('baton: worker P3 has a question for the human. Now put it to them: ' + CALL +
             '. baton acts on the answer itself; then carry on with your task.')

DEFAULTS = {  # channel -> (primer, text)
    'A': ('P0', 'R'), 'A2': ('P0', 'R'), 'B': ('P0', 'R'), 'C': ('P0', 'R'), 'D': ('P0', 'D'), 'DS': ('P0', 'D'), 'E': ('P1', 'R1'), 'F': ('P0', 'RF'), 'G': ('P0', 'R'),
}


def sh(*a, cwd=PROJ):
    subprocess.run(a, cwd=cwd, check=True, capture_output=True)


def setup_proj():
    if not os.path.isdir(os.path.join(PROJ, '.git')):
        os.makedirs(PROJ, exist_ok=True)
        open(os.path.join(PROJ, 'calc.py'), 'w').write(
            'def add(a, b):\n    return a + b\n\n\nif __name__ == "__main__":\n    print(add(2, 3))\n')
        open(os.path.join(PROJ, 'README.md'), 'w').write('# calc\n\n- `add(a, b)`: the sum of a and b\n')
        relay = os.path.join(PROJ, 'baton-relay')
        open(relay, 'w').write('#!/bin/sh\n# stands in for `baton relay <worker>`\ncat <<\'EOF\'\n' + RELAY_OUT + '\nEOF\n')
        os.chmod(relay, 0o755)
        sh('git', 'init', '-q')
        sh('git', 'config', 'user.name', 'spike'); sh('git', 'config', 'user.email', 'spike@example.invalid')
        sh('git', 'config', 'commit.gpgsign', 'false')
        sh('git', 'add', '-A'); sh('git', 'commit', '-qm', 'init'); sh('git', 'tag', 'init')
    sh('git', 'reset', '-q', '--hard', 'init'); sh('git', 'clean', '-qfdx')


class Drv:
    def __init__(self, cwd, argv, rows=50, cols=160):
        self.chunks, self.lock, self.last_out, self.t0 = [], threading.Lock(), time.time(), time.time()
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            os.execvp(argv[0], argv)
        self.pid, self.fd = pid, fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        self.alive = True
        threading.Thread(target=self._pump, daemon=True).start()

    def _pump(self):
        while True:
            r, _, _ = select.select([self.fd], [], [], 0.2)
            if not r:
                continue
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                break
            if not data:
                break
            with self.lock:
                self.chunks.append(data); self.last_out = time.time()
        self.alive = False

    def note(self, msg):
        print(f'[{time.time() - self.t0:6.1f}s] {msg}', flush=True)

    def plain(self, n=40000, every=False):
        with self.lock:
            b = b''.join(self.chunks if every else self.chunks[-600:])
        return ANSI.sub(b'', b)[-n:].decode('utf-8', 'replace')

    def has(self, needle):
        return needle.replace(' ', '') in self.plain().replace(' ', '')

    def wait(self, cond, timeout, what):
        end = time.time() + timeout
        while time.time() < end:
            if cond():
                return True
            if not self.alive:
                self.note(f'child exited waiting for {what}'); return False
            time.sleep(0.3)
        self.note(f'TIMEOUT waiting for {what}'); return False

    def quiet(self, secs=2.5, timeout=60):
        return self.wait(lambda: time.time() - self.last_out >= secs, timeout, f'{secs}s quiet')

    def type(self, text, delay=0.008):
        for ch in text:
            os.write(self.fd, ch.encode()); time.sleep(delay)

    def enter(self):
        time.sleep(0.5); os.write(self.fd, b'\r')

    def close(self):
        for s in (signal.SIGTERM, signal.SIGKILL):
            for kill in (lambda: os.killpg(self.pid, s), lambda: os.kill(self.pid, s)):
                try:
                    kill()
                except (ProcessLookupError, PermissionError):
                    pass
            time.sleep(1.5)
        try:
            os.waitpid(self.pid, os.WNOHANG)
        except ChildProcessError:
            pass


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


def norm(s):
    return ' '.join((s or '').split())


def classify(asks, q=Q, labels=LABELS):
    if not asks:
        return 'no-ask'
    for a in asks:
        qs = (a.get('input') or {}).get('questions') or []
        if len(qs) == 1 and norm(qs[0].get('question')) == norm(q) and [o.get('label') for o in qs[0].get('options') or []] == labels \
                and not qs[0].get('multiSelect'):
            return 'exact'
    return 'reworded'


def words_after(transcript, since):
    """Assistant text blocks (and tool calls) from the transcript, after the request was delivered."""
    out = []
    for r in recs(transcript):
        if r.get('type') != 'assistant':
            continue
        ts = r.get('timestamp')
        try:
            t = calendar.timegm(time.strptime(ts[:19], '%Y-%m-%dT%H:%M:%S')) if ts else 0
        except Exception:
            t = 0
        for b in (r.get('message') or {}).get('content') or []:
            if b.get('type') == 'text' and t >= since - 1:
                out.append(b['text'])
            elif b.get('type') == 'tool_use' and t >= since - 1:
                out.append(f'<tool_use {b.get("name")} {json.dumps(b.get("input"))[:160]}>')
    return out


def run(channel, model, tag, primer_key=None, text_key=None):
    primer_key = primer_key or DEFAULTS[channel][0]
    text_key = text_key or DEFAULTS[channel][1]
    rundir = os.path.join(HERE, 'runs', tag)
    shutil.rmtree(rundir, ignore_errors=True); os.makedirs(rundir)
    setup_proj()
    cfg = {'channel': channel, 'model': model, 'primer_key': primer_key, 'text_key': text_key,
           'primer': PRIMERS[primer_key], 'text': TEXTS[text_key], 'fire_at': 2}
    json.dump(cfg, open(os.path.join(rundir, 'cfg.json'), 'w'), indent=1)
    events = ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop', 'Notification']
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 "{HOOK}" {ev} "{rundir}"'}]}] for ev in events}
    if channel == 'G':  # as internal/host/settings.go configures baton's rewake hooks
        hooks['Stop'].append({'hooks': [{'type': 'command', 'command': f'python3 "{HOOK}" StopRewake "{rundir}"',
                                         'asyncRewake': True, 'timeout': 60}]})
    settings = json.dumps({'hooks': hooks})
    env = ['env', '-i', f'HOME={os.environ["HOME"]}', f'USER={os.environ.get("USER", "")}', f'LOGNAME={os.environ.get("LOGNAME", "")}',
           f'PATH={os.environ["PATH"]}', f'TMPDIR={os.environ.get("TMPDIR", "/tmp")}', f'LANG={os.environ.get("LANG") or "en_US.UTF-8"}',
           f'SHELL={os.environ.get("SHELL", "/bin/zsh")}', 'TERM=xterm-256color']
    argv = env + ['claude', '--model', model, '--permission-mode', 'acceptEdits', '--settings', settings,
                  '--allowedTools', 'Bash,Read,Edit,Write,Glob,Grep,ToolSearch']
    log = os.path.join(rundir, 'hooks.jsonl')
    d = Drv(PROJ, argv)
    d.note(f'{tag}: channel {channel} model {model} primer {primer_key} text {text_key} pid {d.pid}')
    tout = 300 if model == 'sonnet' else 240
    try:
        started = lambda: any(r['ev'] == 'SessionStart' for r in recs(log))
        trust = lambda: d.has('trust this folder') or d.has('Do you trust')
        d.wait(lambda: started() or trust(), 40, 'SessionStart or trust dialog')
        if not started() and trust():
            time.sleep(1)
            scr = d.plain(3000)
            open(os.path.join(rundir, 'trust-screen.txt'), 'w').write(scr)
            # options: "❯ No, exit" (highlighted) then "Yes, I trust this folder": Down, then Enter
            d.note('trust dialog: Down + Enter (Yes, I trust this folder)')
            os.write(d.fd, b'\x1b[B'); time.sleep(0.5); os.write(d.fd, b'\r')
            d.wait(started, 30, 'SessionStart after trust')
        d.quiet(3, 40)
        d.note('typing task'); d.type(TASK); d.enter()
        allowed = lambda: sum(1 for r in recs(log) if r['ev'] == 'Stop' and r['action'] == 'allow')
        d.wait(lambda: allowed() >= 1, tout, 'first allowed Stop')
        d.quiet(3, 30)
        if channel == 'G':
            d.note('idle: waiting for the rewake turn')
            d.wait(lambda: allowed() >= 2, tout, 'second allowed Stop (rewake turn)')
            d.quiet(3, 30)
        if channel == 'C':
            d.note('idle: typing the [baton] line'); d.type(IDLE_LINE); d.enter()
            d.wait(lambda: allowed() >= 2, tout, 'second allowed Stop')
            d.quiet(3, 30)
        open(os.path.join(rundir, 'screen.txt'), 'w').write(d.plain(10**7, every=True))
    finally:
        d.close()
        d.note(f'closed; alive={d.alive}')
    rs = recs(log)
    fired = next((r['t'] for r in rs if r['action'] == 'fired'), None)
    tpath = next((r['transcript'] for r in rs if r.get('transcript')), None)
    if tpath and os.path.exists(tpath):
        shutil.copy(tpath, os.path.join(rundir, 'transcript.jsonl'))
    asks = recs(os.path.join(rundir, 'asks.jsonl'))
    res = {'tag': tag, 'channel': channel, 'model': model, 'primer': primer_key, 'text': text_key,
           'fired': bool(fired), 'asks': len(asks), 'class': classify(asks, *((WQ, WLABELS) if text_key == 'W' else (Q, LABELS))),
           'ask_inputs': [a.get('input') for a in asks],
           'words': words_after(os.path.join(rundir, 'transcript.jsonl'), fired or 0) if tpath else []}
    json.dump(res, open(os.path.join(rundir, 'result.json'), 'w'), indent=1)
    print(json.dumps({k: res[k] for k in ('tag', 'channel', 'model', 'fired', 'asks', 'class')}))
    for w in res['words']:
        print('   >', w.replace('\n', ' | ')[:700])
    return res


if __name__ == '__main__':
    run(*sys.argv[1:])
