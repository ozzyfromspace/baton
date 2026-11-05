#!/usr/bin/env python3
"""Spike 45: a background shell's output file, and stopping one (Claude Code 2.1.318, Haiku, macOS).

Usage: run.py <workroot> [where] [taskstop] [killpg] [tmpdir] (all four when none is named). Spike 43's harness:
claude in a PTY in a fresh git repo, no user settings (--setting-sources project,local), acceptEdits with exact
allow rules, hook.py on the settings events (hooks.jsonl), and a mod (mod/, copied per run) with one command,
/spk-stop <task id>, that calls TaskStop from a timer as baton's mod runs its `stop` command, and that logs every
prompt Claude Code submits, task notifications too (mod.jsonl.*). SPK_CLAUDE names the claude binary.

  where     the lead starts `./job.sh A 20` in the background, and a general-purpose subagent of its starts
            `./job.sh B 20`: what PostToolUse's tool_response and the task notification say of each task's output
            file, and where each file is.
  taskstop  the lead starts `./loop.sh L1` in the background (a poll loop on a file that never appears, with a
            `sleep 600 &` child, writing nothing); /spk-stop ends it: which processes are left, what the mod's call
            returns, whether the model is told or woken, and what the next Stop lists.
  killpg    the same with `./loop.sh L2`, its shell found among claude's children by its command line and its
            process group sent SIGTERM by hand.
  tmpdir    a second session with CLAUDE_CODE_TMPDIR set: where its background output goes.
"""
import glob, json, os, re, shlex, shutil, signal, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HOOK = os.path.join(HERE, 'hook.py')
MOD = os.path.join(HERE, 'mod')
CLAUDE = os.environ.get('SPK_CLAUDE') or 'claude'
EVENTS = ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure', 'Stop',
          'SubagentStart', 'SubagentStop', 'Notification']
ALLOW = ('Bash(./job.sh A 20)', 'Bash(./job.sh B 20)', 'Bash(./job.sh C 5)', 'Bash(./loop.sh L1)', 'Bash(./loop.sh L2)',
         'Agent', 'TaskStop')
JOB = '''#!/bin/sh
# usage: job.sh NAME SECS: a line a second for SECS seconds
echo $$ > "pid-$1"
i=0
while [ $i -lt "$2" ]; do echo "JOB_$1 tick $i"; i=$((i+1)); sleep 1; done
date +%s > "done-$1"
echo "JOB_$1_FINISHED"
'''
LOOP = '''#!/bin/sh
# usage: loop.sh NAME: polls for a file that never appears, writing nothing, with a child of its own
echo $$ > "pid-$1"
sleep 600 &
echo $! > "child-$1"
until [ -f "never-$1" ]; do sleep 2; done
'''
NOTE = re.compile(r'<task-id>(?P<id>[^<]*)</task-id>.*?<output-file>(?P<file>[^<]*)</output-file>\s*<status>(?P<status>[^<]*)</status>\s*<summary>(?P<summary>[^<]*)</summary>', re.S)


def git(cwd, *a):
    return subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, capture_output=True, text=True)


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


def table():
    out = []
    for line in subprocess.run(['ps', '-axo', 'pid=,ppid=,pgid=,command='], capture_output=True, text=True).stdout.splitlines():
        f = line.split(None, 3)
        if len(f) == 4:
            out.append({'pid': int(f[0]), 'ppid': int(f[1]), 'pgid': int(f[2]), 'command': f[3]})
    return out


def alive(pid):
    if not pid:
        return None
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def read_int(path):
    try:
        return int(open(path).read().strip())
    except Exception:
        return None


class Run:
    def __init__(self, root):
        self.work = os.path.join(root, '45')
        shutil.rmtree(self.work, ignore_errors=True)
        self.repo = os.path.join(self.work, 'repo')
        os.makedirs(self.repo)
        git(self.repo, 'init', '-q', '-b', 'main')
        for name, text in (('job.sh', JOB), ('loop.sh', LOOP)):
            p = os.path.join(self.repo, name)
            open(p, 'w').write(text)
            os.chmod(p, 0o755)
        open(os.path.join(self.repo, '.gitignore'), 'w').write('screen.*\npid-*\nchild-*\ndone-*\n')
        git(self.repo, 'add', '.')
        git(self.repo, 'commit', '-qm', 'init')
        self.mod = os.path.join(self.work, 'mod')
        shutil.copytree(MOD, self.mod)
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.modlog = os.path.join(self.work, 'mod.jsonl')
        log = shlex.quote(self.hooks)
        self.settings = json.dumps({'hooks': {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {log}'}]}] for ev in EVENTS}})
        self.version = subprocess.run([CLAUDE, '--version'], capture_output=True, text=True).stdout.strip()
        self.d = None
        self.label = None

    def open(self, label, flags, env=None, trust=True):
        self.label = label
        argv = [CLAUDE, *flags, '--setting-sources', 'project,local', '--permission-mode', 'acceptEdits',
                '--plugin-dir', self.mod, '--settings', self.settings]
        for a in ALLOW:
            argv += ['--allowedTools', a]
        self.mark(f'open {label}')
        self.d = Driver(self.repo, argv, env_extra={'SPK_LOG': self.modlog, **(env or {})})
        if trust:
            self.d.trust(25)
        self.d.wait(lambda: self.ev('SessionStart', lambda x: float(x['t']) > self.opened), 40, 'SessionStart')
        self.d.wait_quiet(3, 40)

    def close(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()
        time.sleep(1)
        shutil.move(os.path.join(self.repo, 'screen.log'), os.path.join(self.work, f'screen-{self.label}.log'))

    def mark(self, label):
        self.opened = time.time()
        with open(self.hooks, 'a') as f:
            f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'input': {'step': label}}) + '\n')
        print(f'[{time.strftime("%H:%M:%S")}] {label}', flush=True)

    def cmd(self, text):
        self.mark('type: ' + text[:120])
        self.d.type(text, delay=0.01)
        time.sleep(0.6)
        self.d.enter()
        time.sleep(1.5)

    def ev(self, name, pred=lambda r: True):
        return [r for r in recs(self.hooks) if r['ev'] == name and pred(r)]

    def stops(self):
        return len(self.ev('Stop'))

    def ask(self, text, timeout=150):
        n = self.stops()
        self.cmd(text)
        ok = self.d.wait(lambda: self.stops() > n, timeout, 'lead stop')
        self.d.wait_quiet(2, 20)
        return ok

    def modrecs(self, kind=None):
        out = []
        for p in sorted(glob.glob(self.modlog + '.*')):
            out += recs(p)
        return [r for r in sorted(out, key=lambda r: r['t']) if kind is None or r['kind'] == kind]

    def session(self):
        s = self.ev('SessionStart')
        i = s[-1]['input'] if s else {}
        return i.get('session_id'), i.get('transcript_path')

    def started(self, needle):
        """The PostToolUse of the Bash call whose command holds needle and that started a background task."""
        for r in self.ev('PostToolUse', lambda x: x['input'].get('tool_name') == 'Bash'):
            i = r['input']
            if needle in (i.get('tool_input') or {}).get('command', '') and (i.get('tool_response') or {}).get('backgroundTaskId'):
                return i
        return None

    def notes(self, task):
        """What the model was told of task, from the prompts the mod saw (task notifications)."""
        out = []
        for r in self.modrecs('prompt'):
            for m in NOTE.finditer(r.get('text') or ''):
                if m['id'] == task:
                    out.append({'via': 'mod', 'origin': r.get('origin'), **m.groupdict()})
        for r in self.ev('UserPromptSubmit'):
            for m in NOTE.finditer(r['input'].get('prompt') or ''):
                if m['id'] == task:
                    out.append({'via': 'UserPromptSubmit', **m.groupdict()})
        return out


def expected(sid, transcript, task, root='/tmp'):
    slug = os.path.basename(os.path.dirname(transcript or ''))
    return os.path.join(root, f'claude-{os.getuid()}', slug, sid or '', 'tasks', f'{task}.output')


def where(r):
    r.ask('Spike test of background jobs. Do these two things, then end your turn. (1) Use the Bash tool with '
          'run_in_background set to true to run exactly: ./job.sh A 20   (2) Use the Agent tool with subagent_type '
          'general-purpose and this prompt: "Use the Bash tool with run_in_background set to true to run exactly: '
          './job.sh B 20   Do not wait for it. Then reply with only the word STARTED." Do not wait for either job, '
          'do not poll, do not sleep. Then reply with only the word started.')
    r.d.wait(lambda: all(os.path.exists(os.path.join(r.repo, f'done-{n}')) for n in 'AB'), 90, 'jobs A and B done')
    time.sleep(15)
    r.d.wait_quiet(3, 30)
    sid, tp = r.session()
    out = {'session': sid, 'transcript': tp, 'tasks': {}}
    for name in 'AB':
        i = r.started(f'./job.sh {name}') or {}
        tid = (i.get('tool_response') or {}).get('backgroundTaskId')
        path = expected(sid, tp, tid) if tid else None
        out['tasks'][name] = {
            'task': tid, 'agent_id': i.get('agent_id'), 'agent_type': i.get('agent_type'), 'hook_session': i.get('session_id'),
            'hook_transcript': i.get('transcript_path'), 'cwd': i.get('cwd'),
            'tool_response': i.get('tool_response'),
            'notified': r.notes(tid) if tid else [],
            'expected_path': path, 'exists': bool(path and os.path.exists(path)),
            'content_tail': open(path).read()[-200:] if path and os.path.exists(path) else None,
            'outlived_its_caller': os.path.exists(os.path.join(r.repo, f'done-{name}')),
        }
    tasks = os.path.dirname(expected(sid, tp, 'x'))
    out['tasks_dir'] = tasks
    out['tasks_dir_listing'] = sorted(os.listdir(tasks)) if os.path.isdir(tasks) else None
    out['subagent_stops'] = [x['input'].get('agent_id') for x in r.ev('SubagentStop')]
    return out


def secs(t):
    """A mod record's time ($.clock.now()) in seconds."""
    t = float(t)
    return t / 1000 if t > 1e11 else t


def stopping(r, name, how):
    t0 = time.time()
    r.ask(f'Use the Bash tool with run_in_background set to true to run exactly: ./loop.sh {name}   Do not wait for it, '
          'do not poll it, do not sleep. Then reply with only the word started and end your turn.')
    i = r.started(f'./loop.sh {name}') or {}
    tid = (i.get('tool_response') or {}).get('backgroundTaskId')
    time.sleep(2)
    claude = r.d.pid
    procs = table()
    claude_pgid = next((p['pgid'] for p in procs if p['pid'] == claude), None)
    shell = next((p for p in procs if p['ppid'] == claude and f'loop.sh {name}' in p['command']), None)
    loop, child = read_int(os.path.join(r.repo, f'pid-{name}')), read_int(os.path.join(r.repo, f'child-{name}'))
    group = [p for p in procs if shell and p['pgid'] == shell['pgid']]
    before = {'claude': claude, 'claude_pgid': claude_pgid, 'shell': shell, 'loop': loop, 'child': child,
              'group': [{k: p[k] for k in ('pid', 'ppid', 'command')} for p in group]}
    n = r.stops()
    stopped = None
    if how == 'taskstop':
        r.cmd(f'/spk-stop {tid}')
        r.d.wait(lambda: r.modrecs('stopped') or r.modrecs('stop-failed'), 30, 'the mod\'s TaskStop')
        stopped = (r.modrecs('stopped') + r.modrecs('stop-failed'))[-1:]
    elif shell and shell['pgid'] != claude_pgid:
        r.mark(f'killpg {shell["pgid"]}')
        os.killpg(shell['pgid'], signal.SIGTERM)
    time.sleep(3)
    after = {str(p): alive(p) for p in [shell and shell['pid'], loop, child] + [g['pid'] for g in group] if p}
    woke = r.d.wait(lambda: r.stops() > n, 45, 'a turn the stop woke')
    r.d.wait_quiet(3, 20)
    r.ask('Reply with only the word OK.')
    last = r.ev('Stop')[-1]['input'] if r.ev('Stop') else {}
    sid, tp = r.session()
    path = expected(sid, tp, tid) if tid else None
    return {'task': tid, 'before': before, 'mod_call': stopped, 'alive_after': after, 'woke': woke,
            'notified': r.notes(tid) if tid else [], 'next_stop_background_tasks': last.get('background_tasks'),
            'output': open(path).read()[-300:] if path and os.path.exists(path) else None,
            'prompts_since': [{'origin': x.get('origin'), 'text': (x.get('text') or '')[:400]} for x in r.modrecs('prompt') if secs(x['t']) >= t0]}


def tmpdir(r):
    tmp = os.path.join(r.work, 'tmp')
    os.makedirs(tmp, exist_ok=True)
    r.open('T', ['--model', 'haiku'], env={'CLAUDE_CODE_TMPDIR': tmp}, trust=False)
    r.ask('Use the Bash tool with run_in_background set to true to run exactly: ./job.sh C 5   Do not wait for it, do not '
          'poll it, do not sleep. Then reply with only the word started and end your turn.')
    r.d.wait(lambda: os.path.exists(os.path.join(r.repo, 'done-C')), 40, 'job C done')
    time.sleep(8)
    i = r.started('./job.sh C') or {}
    tid = (i.get('tool_response') or {}).get('backgroundTaskId')
    sid, tp = r.session()
    found = glob.glob(os.path.join(tmp, '**', f'{tid}.output'), recursive=True) if tid else []
    out = {'tmpdir': tmp, 'task': tid, 'found': found, 'expected_under_tmpdir': expected(sid, tp, tid, tmp) if tid else None,
           'under_tmp': os.path.exists(expected(sid, tp, tid)) if tid else None, 'notified': r.notes(tid) if tid else []}
    r.close()
    return out


def main(root, qs):
    qs = qs or ['where', 'taskstop', 'killpg', 'tmpdir']
    r = Run(root)
    print('claude', r.version, 'work', r.work, flush=True)
    out = {'claude': r.version}
    if set(qs) & {'where', 'taskstop', 'killpg'}:
        r.open('S', ['--model', 'haiku'])
        if 'where' in qs:
            out['where'] = where(r)
        if 'taskstop' in qs:
            out['taskstop'] = stopping(r, 'L1', 'taskstop')
        if 'killpg' in qs:
            out['killpg'] = stopping(r, 'L2', 'killpg')
        out['session'] = r.session()
        r.close()
        for name in ('L1', 'L2'):  # whatever is left of the loops
            for f in ('pid', 'child'):
                p = read_int(os.path.join(r.repo, f'{f}-{name}'))
                if p and alive(p):
                    os.kill(p, signal.SIGKILL)
    if 'tmpdir' in qs:
        out['tmpdir'] = tmpdir(r)
    path = os.path.join(r.work, 'results.json')
    json.dump(out, open(path, 'w'), indent=1)
    print(json.dumps(out, indent=1))
    print('results:', path)


if __name__ == '__main__':
    main(sys.argv[1], sys.argv[2:])
