#!/usr/bin/env python3
"""Spike: what a rewind (Esc Esc, /rewind) leaves behind (Claude Code 2.1.296, Haiku). Usage: run.py <scenario>

Every hook event is logged with what the transcript held as it ran (hook.py), and each step's screen is
dumped to work/<scenario>/screen-<label>.txt.
"""
import json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HOOK = os.path.join(HERE, 'hook.py')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact']
UP, DOWN, ESC = b'\x1b[A', b'\x1b[B', b'\x1b'


def setup(name):
    work = os.path.join(HERE, 'work', name)
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    subprocess.run(['git', 'init', '-q'], cwd=work)
    open(os.path.join(work, 'README'), 'w').write('spike\n')
    subprocess.run(['git', 'add', '.'], cwd=work)
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', 'commit', '-qm', 'init'], cwd=work)
    log = os.path.join(work, 'hooks.jsonl')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {shlex.quote(log)}'}]}] for ev in EVENTS}
    return work, log, json.dumps({'hooks': hooks})


def recs(log):
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []


def count(log, ev):
    return sum(1 for r in recs(log) if r['ev'] == ev)


def dump(d, label, n=4000):
    time.sleep(1.0)
    with d.lock:
        t = d.tail[-n:].decode('utf-8', 'replace')
    with open(os.path.join(d.cwd, f'screen-{label}.txt'), 'w') as f:
        f.write(t)
    d.note(f'screen dumped: {label}')


def start(name):
    work, log, settings = setup(name)
    d = Driver(work, ['claude', '--model', 'haiku', '--permission-mode', 'acceptEdits',
                      '--allowedTools', 'Bash(echo:*)', '--settings', settings])
    d.trust(25)
    d.wait_quiet(3, 40)
    return d, work, log


def ask(d, log, text, timeout=120):
    n = count(log, 'Stop') + count(log, 'StopFailure')
    d.type(text); d.enter()
    ok = d.wait(lambda: count(log, 'Stop') + count(log, 'StopFailure') > n, timeout, f'stop after: {text[:30]}')
    d.wait_quiet(2, 20)
    return ok


def three_turns(d, log):
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo one')
    ask(d, log, 'Use the Write tool to create the file a.txt containing exactly the word alpha. Do nothing else.')
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo three')


def finish(d):
    d.type('/exit'); d.enter(); time.sleep(4); d.close()


def mark(log, label):
    with open(log, 'a') as f:
        f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'input': {'step': label}}) + '\n')


def explore():
    """Open the rewind menu after three turns and dump each screen on the way: what it lists, what a
    message's options are. Leaves without choosing."""
    d, work, log = start('explore')
    three_turns(d, log)
    mark(log, 'esc esc')
    d.key(ESC); time.sleep(0.3); d.key(ESC); dump(d, 'menu')
    d.key(UP); dump(d, 'up1')
    d.key(UP); dump(d, 'up2')
    d.key(b'\r'); dump(d, 'chosen')
    d.key(ESC); dump(d, 'esc1')
    d.key(ESC); dump(d, 'esc2')
    finish(d)


def rewind(d, log, ups, option, label):
    """Esc Esc, select the message ups above (current), confirm, and pick option (1-5)."""
    mark(log, f'rewind: up {ups}, option {option}')
    d.key(ESC); time.sleep(0.3); d.key(ESC); time.sleep(1.5)
    for _ in range(ups):
        d.key(UP); time.sleep(0.5)
    dump(d, f'{label}-menu')
    d.key(b'\r'); time.sleep(1.5); dump(d, f'{label}-confirm')
    d.key(option.encode()); time.sleep(2.5); dump(d, f'{label}-after')
    mark(log, 'rewound')


def then_ask(d, log, text, label):
    d.key(b'\x03'); time.sleep(0.8)  # Ctrl-C: empty the input box, which may hold the rewound prompt
    dump(d, f'{label}-cleared')
    ask(d, log, text)
    dump(d, f'{label}-answered')


def summary(work):
    """The session ids seen, a.txt, and the transcript's entries: type, uuid, parent."""
    out = {'a.txt': os.path.exists(os.path.join(work, 'a.txt')) and open(os.path.join(work, 'a.txt')).read()}
    sids = sorted({r['input'].get('session_id') for r in recs(os.path.join(work, 'hooks.jsonl')) if r['input'].get('session_id')})
    out['sessions'] = sids
    tps = sorted({r['input'].get('transcript_path') for r in recs(os.path.join(work, 'hooks.jsonl')) if r['input'].get('transcript_path')})
    out['transcripts'] = tps
    with open(os.path.join(work, 'summary.json'), 'w') as f:
        json.dump(out, f, indent=1)
    for tp in tps:
        shutil.copy(tp, os.path.join(work, os.path.basename(tp)))
    print(json.dumps(out, indent=1))


def A():
    """Rewind to before the Write turn, conversation only, then a new prompt."""
    d, work, log = start('A')
    three_turns(d, log)
    rewind(d, log, 2, '2', 'A')
    then_ask(d, log, 'Run this exact Bash command and reply with only its output: echo four', 'A')
    finish(d); summary(work)


def B():
    """Rewind to before the Write turn, code and conversation, then a new prompt."""
    d, work, log = start('B')
    three_turns(d, log)
    rewind(d, log, 2, '1', 'B')
    then_ask(d, log, 'Run this exact Bash command and reply with only its output: echo four', 'B')
    finish(d); summary(work)


def C():
    """Two turns, /compact, a third: does the menu reach past the compaction?"""
    d, work, log = start('C')
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo one')
    ask(d, log, 'Use the Write tool to create the file a.txt containing exactly the word alpha. Do nothing else.')
    n = sum(1 for r in recs(log) if r['ev'] == 'SessionStart' and r['input'].get('source') == 'compact')
    d.type('/compact'); d.enter()
    d.wait(lambda: sum(1 for r in recs(log) if r['ev'] == 'SessionStart' and r['input'].get('source') == 'compact') > n, 180, 'compaction')
    d.wait_quiet(3, 30)
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo three')
    mark(log, 'esc esc')
    d.key(ESC); time.sleep(0.3); d.key(ESC); time.sleep(1.5); dump(d, 'C-menu')
    for i in range(4):
        d.key(UP); time.sleep(0.5)
    dump(d, 'C-top')
    d.key(b'\r'); time.sleep(1.5); dump(d, 'C-confirm')
    d.key(b'2'); time.sleep(2.5); dump(d, 'C-after')
    mark(log, 'rewound')
    then_ask(d, log, 'Run this exact Bash command and reply with only its output: echo four', 'C')
    finish(d); summary(work)


def C2():
    """Rewind to before a /compact, Restore conversation: does the model get the conversation from before
    the compaction back? A codeword the compaction leaves out tells."""
    d, work, log = start('C2')
    ask(d, log, 'Remember this codeword for later: PELICAN-7. Reply with only the word ok.')
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo two')
    n = sum(1 for r in recs(log) if r['ev'] == 'SessionStart' and r['input'].get('source') == 'compact')
    d.type('/compact Summarize in one short sentence. Leave out every codeword, and say that none was given.'); d.enter()
    d.wait(lambda: sum(1 for r in recs(log) if r['ev'] == 'SessionStart' and r['input'].get('source') == 'compact') > n, 180, 'compaction')
    d.wait_quiet(3, 30)
    ask(d, log, 'What was the codeword? Reply with only the codeword, or UNKNOWN if you do not know it.')
    dump(d, 'C2-control')
    mark(log, 'esc esc')
    d.key(ESC); time.sleep(0.3); d.key(ESC); time.sleep(1.5)
    d.key(UP); time.sleep(0.5); d.key(UP); time.sleep(0.5); dump(d, 'C2-menu')  # the /compact entry, above the latest prompt
    d.key(b'\r'); time.sleep(1.5); dump(d, 'C2-confirm')
    d.key(b'1'); time.sleep(3); dump(d, 'C2-after')  # 1. Restore conversation
    mark(log, 'rewound')
    d.key(b'\x15'); time.sleep(0.3); d.key(b'\x15'); time.sleep(0.5)  # Ctrl-U: empty the input box
    dump(d, 'C2-cleared')
    ask(d, log, 'What was the codeword? Reply with only the codeword, or UNKNOWN if you do not know it.')
    dump(d, 'C2-answered')
    finish(d); summary(work)


if __name__ == '__main__':
    globals()[sys.argv[1]]()
