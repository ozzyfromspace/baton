#!/usr/bin/env python3
"""Spike: what a claude that dies mid-turn leaves, and whether --resume brings it back (Claude Code 2.1.296,
Haiku). Usage: run.py <scenario>. Every hook event is logged with the transcript's tail (hook.py)."""
import json, os, shlex, shutil, signal, subprocess, sys, time, uuid
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HOOK = os.path.join(HERE, 'hook.py')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'Notification', 'Stop', 'StopFailure']


def setup(name):
    work = os.path.join(HERE, 'work', name)
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    subprocess.run(['git', 'init', '-q'], cwd=work)
    log = os.path.join(work, 'hooks.jsonl')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {shlex.quote(log)}'}]}] for ev in EVENTS}
    return work, log, json.dumps({'hooks': hooks})


def recs(log):
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []


def count(log, ev, pred=lambda r: True):
    return sum(1 for r in recs(log) if r['ev'] == ev and pred(r))


def mark(log, label, **kw):
    with open(log, 'a') as f:
        f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'input': {'step': label, **kw}}) + '\n')


def dump(d, label, n=3000):
    time.sleep(1.0)
    with d.lock:
        t = d.tail[-n:].decode('utf-8', 'replace')
    with open(os.path.join(d.cwd, f'screen-{label}.txt'), 'w') as f:
        f.write(t)


def launch(work, settings, *args):
    d = Driver(work, ['claude', '--model', 'haiku', '--allowedTools', 'Bash(sleep:*)', 'Bash(echo:*)', '--settings', settings, *args])
    d.trust(25)
    return d


def ask(d, log, text, timeout=120):
    n = count(log, 'Stop') + count(log, 'StopFailure')
    d.type(text); d.enter()
    ok = d.wait(lambda: count(log, 'Stop') + count(log, 'StopFailure') > n, timeout, f'stop after: {text[:30]}')
    d.wait_quiet(2, 20)
    return ok


def transcript(sid):
    for root, _, files in os.walk(os.path.expanduser('~/.claude/projects')):
        if sid + '.jsonl' in files:
            return os.path.join(root, sid + '.jsonl')
    return None


def tail_of(path, n=6):
    out = []
    for l in open(path).read().splitlines()[-n:]:
        try:
            d = json.loads(l)
        except Exception:
            out.append({'unparsable': l[:80]}); continue
        m = d.get('message') or {}
        c = m.get('content')
        kinds = c[:40] if isinstance(c, str) else ','.join(x.get('type', '') for x in c) if isinstance(c, list) else ''
        out.append({'type': d.get('type'), 'sub': d.get('subtype'), 'content': kinds, 'stop': m.get('stop_reason')})
    return out


def resume_and_ask(work, settings, log, sid, label):
    """Resume sid in a fresh terminal, time its start, and ask one question."""
    t0 = time.time()
    mark(log, f'{label}: resume')
    d = launch(work, settings, '--resume', sid) if os.path.exists(os.path.join(work, '.trusted')) else None
    if d is None:
        d = Driver(work, ['claude', '--model', 'haiku', '--allowedTools', 'Bash(sleep:*)', 'Bash(echo:*)', '--settings', settings, '--resume', sid])
    started = d.wait(lambda: count(log, 'SessionStart', lambda r: r['input'].get('source') == 'resume' and r['t'] > f'{t0:.3f}') > 0, 60, 'SessionStart(resume)')
    res = {'resumed': started, 'start_s': round(time.time() - t0, 1), 'alive': d.alive}
    d.wait_quiet(3, 30); dump(d, f'{label}-resumed')
    if d.alive:
        n = count(log, 'StopFailure')
        res['answered'] = ask(d, log, 'Reply with only the word ok.')
        res['stop_failure'] = count(log, 'StopFailure') > n
        dump(d, f'{label}-answered')
        d.type('/exit'); d.enter(); time.sleep(4)
    else:
        res['exit'] = os.waitpid(d.pid, 0)[1] >> 8
        dump(d, f'{label}-exited')
    d.close()
    return res


def K1():
    """kill -9 while a Bash tool call runs, then resume."""
    work, log, settings = setup('K1')
    sid = str(uuid.uuid4())
    d = launch(work, settings, '--session-id', sid)
    d.wait_quiet(3, 40)
    open(os.path.join(work, 'hold'), 'w').close()  # hook.py holds the tool call: the kill lands mid-turn
    d.type('Run this exact Bash command and reply with only its output: echo done'); d.enter()
    d.wait(lambda: count(log, 'PreToolUse') > 0, 90, 'PreToolUse')
    time.sleep(1.5)
    os.remove(os.path.join(work, 'hold'))
    mark(log, 'K1: kill -9'); os.kill(d.pid, signal.SIGKILL); time.sleep(2)
    out = {'tail_after_kill': tail_of(transcript(sid))}
    out['resume'] = resume_and_ask(work, settings, log, sid, 'K1')
    out['tail_after_resume'] = tail_of(transcript(sid), 10)
    json.dump(out, open(os.path.join(work, 'result.json'), 'w'), indent=1); print(json.dumps(out, indent=1))


def K2():
    """kill -9 while the model writes a long answer, then resume."""
    work, log, settings = setup('K2')
    sid = str(uuid.uuid4())
    d = launch(work, settings, '--session-id', sid)
    d.wait_quiet(3, 40)
    d.type('Write a 600-word story about a lighthouse keeper. Do not use any tools.'); d.enter()
    d.wait(lambda: count(log, 'UserPromptSubmit') > 0, 30, 'UserPromptSubmit')
    time.sleep(2.5)
    mark(log, 'K2: kill -9'); os.kill(d.pid, signal.SIGKILL); time.sleep(2)
    out = {'tail_after_kill': tail_of(transcript(sid))}
    out['resume'] = resume_and_ask(work, settings, log, sid, 'K2')
    out['tail_after_resume'] = tail_of(transcript(sid), 10)
    json.dump(out, open(os.path.join(work, 'result.json'), 'w'), indent=1); print(json.dumps(out, indent=1))


def K3():
    """A transcript whose last line was cut off mid-write: does --resume load it?"""
    work, log, settings = setup('K3')
    sid = str(uuid.uuid4())
    d = launch(work, settings, '--session-id', sid)
    d.wait_quiet(3, 40)
    ask(d, log, 'Run this exact Bash command and reply with only its output: echo one')
    d.type('/exit'); d.enter(); time.sleep(4); d.close()
    tp = transcript(sid)
    lines = open(tp).read().splitlines()
    half = lines[-1][:len(lines[-1]) // 2]
    open(tp, 'a').write(lines[-2][:len(lines[-2]) // 2])  # a line cut off mid-write, no newline
    mark(log, 'K3: truncated line appended', bytes=len(lines[-2]) // 2)
    out = {'resume': resume_and_ask(work, settings, log, sid, 'K3'), 'tail_after_resume': tail_of(tp, 8)}
    json.dump(out, open(os.path.join(work, 'result.json'), 'w'), indent=1); print(json.dumps(out, indent=1))


def K4():
    """--resume of a session whose transcript does not exist: how claude fails, and how fast."""
    work, log, settings = setup('K4')
    out = {'resume': resume_and_ask(work, settings, log, str(uuid.uuid4()), 'K4')}
    json.dump(out, open(os.path.join(work, 'result.json'), 'w'), indent=1); print(json.dumps(out, indent=1))


if __name__ == '__main__':
    globals()[sys.argv[1]]()
