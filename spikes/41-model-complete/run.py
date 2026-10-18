#!/usr/bin/env python3
"""Spike 41: $.model.complete from the mod's poll (Claude Code 2.1.302, Haiku).

Usage: run.py <workroot>. Runs an interactive claude (--model haiku) in a PTY in a fresh scratch project
(<workroot>/repo, which sets plansDirectory and allows one exact Bash command), with only this spike's mod (mod/,
loaded with --plugin-dir), no user settings (--setting-sources project,local), and no CLAUDE* or BATON*
variables. It writes numbered command files to <workroot>/cmd/<n>.json, which the mod's poll runs as baton's
bridge commands run, each a $.model.complete call the poll does not wait for:

  idle      three Haiku calls 5 s apart, before any prompt (the lead idle)
  turn      the lead is given one prompt that runs `sh slow.sh` (a 25 s sleep: Claude Code refuses a bare `sleep 25`); 6 s in, while that turn runs: two Haiku calls;
            an empty text block (the API's 400); timeoutMs 1; a model id that does not exist; maxTokens past
            the limit; `opus` and `sonnet`
  after     two Haiku calls once the turn has ended; then calls the size P14 and P19 plan: about 12,000 characters
            of a made-up gate log to Haiku (maxTokens 400), and about 30,000 to Opus (maxTokens 800)

It then prints the mod's log (<workroot>/mod.log: each call's result and latency, the lead's turns, every row the
session appended), and looks for the calls' marker (SPK41) in the session's transcript and on the screen.
Stdlib only; no baton involved. Each run costs a few cents.
"""
import glob, json, os, pty, select, shutil, signal, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
work = os.path.abspath(sys.argv[1])
shutil.rmtree(work, ignore_errors=True)
repo = os.path.join(work, 'repo')
cmds = os.path.join(work, 'cmd')
os.makedirs(os.path.join(repo, '.claude'))
os.makedirs(cmds)
subprocess.run(['git', 'init', '-q', '-b', 'main'], cwd=repo, check=True)
open(os.path.join(repo, 'README'), 'w').write('spike 41\n')
open(os.path.join(repo, 'slow.sh'), 'w').write('sleep 25\necho slept\n')
open(os.path.join(repo, '.claude', 'settings.json'), 'w').write(json.dumps({'plansDirectory': 'plans', 'permissions': {'allow': ['Bash(sh slow.sh)']}}))
log = os.path.join(work, 'mod.log')

env = {k: v for k, v in os.environ.items() if not k.startswith('CLAUDE') and not k.startswith('BATON')}
env.update({'BATON_HOME': os.path.join(work, 'bh'), 'SPK_LOG': log, 'SPK_DIR': work, 'TERM': 'xterm-256color', 'COLUMNS': '120', 'LINES': '40'})
argv = ['claude', '--model', 'haiku', '--setting-sources', 'project,local', '--plugin-dir', os.path.join(HERE, 'mod')]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(repo)
    os.execvpe(argv[0], argv, env)

screen = open(os.path.join(work, 'screen.raw'), 'wb')
n = 0
SYSTEM = 'You answer in one short line.'


def pump(secs):
    end = time.time() + secs
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                data = os.read(fd, 65536)
            except OSError:
                return
            if not data:
                return
            screen.write(data)
            screen.flush()


def send(b):
    os.write(fd, b)


def cmd(label, **c):
    global n
    n += 1
    c.setdefault('model', 'haiku')
    c.setdefault('system', SYSTEM)
    c.setdefault('prompt', f'Reply with only the word PONG-{n}. (SPK41)')
    tmp = os.path.join(cmds, f'.{n}.tmp')
    open(tmp, 'w').write(json.dumps({'label': label, **c}))
    os.rename(tmp, os.path.join(cmds, f'{n}.json'))


pump(8)
send(b'\x1b[B')  # the trust dialog: down to "Yes, I trust this folder"
pump(1)
send(b'\r')
pump(5)
for i in range(3):
    cmd('idle haiku')
    pump(5)
pump(5)
for ch in 'Run the Bash command `sh slow.sh` (it takes 25 seconds), then reply with only the word DONE.':
    send(ch.encode())
    time.sleep(0.02)
pump(1)
send(b'\r')
pump(6)
cmd('turn haiku')
cmd('turn haiku')
cmd('empty block', prompt=[{'text': ''}])
cmd('timeout 1 ms', timeoutMs=1)
cmd('no such model', model='claude-no-such-model-1')
cmd('maxTokens past the limit', maxTokens=200000)
cmd('opus', model='opus', maxTokens=50)
cmd('sonnet', model='sonnet', maxTokens=50)
pump(45)
cmd('after haiku')
pump(3)
cmd('after haiku')
pump(5)
LOG_LINE = 'ok  \tgithub.com/x/y/internal/pkg{0}\t{0}.{0}s\n'
log_text = ''.join(LOG_LINE.format(i % 97) for i in range(400))
fail = '--- FAIL: TestLanding (0.31s)\n    merge_test.go:212: want landed, got held\nFAIL\n'
ask = ('Is this failure the change\'s or the environment\'s? What is the smallest next check? Answer in at most '
       '600 characters. (SPK41)\n\n<log>\n')
cmd('advisor size, haiku', prompt=ask + (log_text + fail)[-11500:] + '</log>', maxTokens=400)
pump(3)
cmd('risk size, opus', model='opus', prompt=('First word: continue, targeted or full; then why. (SPK41)\n\n<diffs>\n' + (log_text * 3)[:29500] + fail + '</diffs>'), maxTokens=800)
pump(60)
os.kill(pid, signal.SIGTERM)
pump(2)
try:
    os.waitpid(pid, 0)
except Exception:
    pass

text = open(log).read() if os.path.exists(log) else '(no log)'
print(text)
sid = ''
for line in text.splitlines():
    if 'session.start ' in line:
        sid = line.split('session.start ')[1].strip()
found = glob.glob(os.path.expanduser(f'~/.claude/projects/*/{sid}.jsonl')) if sid else []
print('transcript:', found)
for path in found:
    rows = [json.loads(l) for l in open(path) if l.strip()]
    print('rows:', len(rows), 'by type:', {t: sum(1 for r in rows if r.get('type') == t) for t in sorted({r.get('type') for r in rows})})
    raw = open(path).read()
    print('SPK41 in transcript:', raw.count('SPK41'), ' PONG in transcript:', raw.count('PONG'))
    for r in rows:
        if r.get('type') == 'cost-state':
            print('cost-state models:', {m: (u.get('inputTokens'), u.get('outputTokens')) for m, u in r.get('modelUsage', {}).items()})
    print('user prompts:', [r['message']['content'] if isinstance(r['message']['content'], str) else '[blocks]' for r in rows if r.get('type') == 'user' and not r.get('isMeta') and isinstance(r.get('message', {}).get('content'), str)])
print('SPK41 on screen:', open(os.path.join(work, 'screen.raw'), 'rb').read().count(b'SPK41'))
