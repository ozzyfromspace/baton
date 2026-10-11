#!/usr/bin/env python3
"""Spike: background work vs compaction and restart (Claude Code 2.1.296, Haiku). Usage: run.py q1|q3|q4"""
import json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HL = os.path.join(HERE, 'hooklog.py')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact', 'SubagentStart', 'SubagentStop',
          'TaskCreated', 'TaskCompleted', 'PermissionRequest']
JOB = '#!/bin/sh\n# usage: job.sh NAME SECS\necho $$ > "pid-$1"\nsleep "$2"\ndate +%s > "done-$1"\necho "JOB_$1_FINISHED"\n'

def setup(name, reuse=None):
    work = os.path.join(HERE, 'work', reuse or name)
    if not reuse:
        shutil.rmtree(work, ignore_errors=True)
        os.makedirs(work)
        subprocess.run(['git', 'init', '-q'], cwd=work)
        open(os.path.join(work, 'job.sh'), 'w').write(JOB); os.chmod(os.path.join(work, 'job.sh'), 0o755)
    log = os.path.join(work, f'hooks-{name}.log')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HL)} {ev} --log {shlex.quote(log)}'}]}] for ev in EVENTS}
    s = {'hooks': hooks, 'permissions': {'allow': ['Bash', 'Monitor', 'Read', 'CronCreate', 'CronList', 'ToolSearch']}}
    return work, log, json.dumps(s)

def recs(log):
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []

def count(log, ev, pred=lambda r: True):
    return sum(1 for r in recs(log) if r['ev'] == ev and pred(r))

def dump(d, label, n=4000):
    with d.lock:
        t = d.tail[-n:].decode('utf-8', 'replace')
    open(os.path.join(d.cwd, f'screen-{label}.txt'), 'w').write(t)
    d.note(f'screen dumped: {label}')

def prompt(d, text):
    d.type(text, delay=0.01); d.enter()

def wait_stop(d, log, n, timeout=150, what='Stop'):
    ok = d.wait(lambda: count(log, 'Stop') > n, timeout, what)
    d.wait_quiet(2, 20)
    return ok

def start(name, args=(), reuse=None):
    work, log, settings = setup(name, reuse)
    d = Driver(work, ['claude', '--model', 'haiku', '--settings', settings, *args])
    d.trust(25)
    d.wait_quiet(3, 40)
    return d, work, log

def sid(log):
    for r in recs(log):
        s = r['input'].get('session_id')
        if s: return s

def ps_job(work, name):
    try:
        pid = int(open(os.path.join(work, f'pid-{name}')).read().strip())
    except Exception:
        return None, 'no pid file'
    r = subprocess.run(['ps', '-o', 'pid,ppid,pgid,stat,command', '-p', str(pid)], capture_output=True, text=True)
    return pid, r.stdout.strip()

PURPOSE = ('This background job stands in for the P4 end-to-end test suite of the Zebra project, and when it finishes '
           'the next step is to open phase P5 (the billing page).')

def q1():
    d, work, log = start('q1')
    n = count(log, 'Stop')
    prompt(d, 'Spike test of background jobs. Use the Bash tool with run_in_background set to true to run exactly: ./job.sh A 100   '
              + PURPOSE + ' Do not wait for it, do not poll it, do not sleep. After starting it, reply with the single word "started" and end your turn.')
    wait_stop(d, log, n, what='Stop after start')
    d.note(f'job A: {ps_job(work, "A")}')
    dump(d, 'q1-started')
    time.sleep(3)
    d.note('typing /compact'); d.type('/compact'); d.enter()
    d.wait(lambda: count(log, 'PostCompact') > 0, 180, 'PostCompact')
    d.wait_quiet(3, 30)
    d.note(f'after compact job A: {ps_job(work, "A")}')
    dump(d, 'q1-compacted')
    n = count(log, 'Stop')
    # wait for the job to finish and see whether the model is woken on its own
    d.wait(lambda: os.path.exists(os.path.join(work, 'done-A')), 150, 'job A done file')
    d.note('job A finished; waiting up to 90s for an unprompted turn')
    woke = d.wait(lambda: count(log, 'Stop') > n, 90, 'Stop of a self-woken turn')
    d.wait_quiet(3, 30)
    dump(d, 'q1-after-job')
    d.note(f'self-woken: {woke}')
    n = count(log, 'Stop')
    prompt(d, 'Without running any tools: what was the background job you started earlier standing in for, and what is the next step now that it has finished? Answer in one or two lines.')
    wait_stop(d, log, n, what='Stop after question')
    dump(d, 'q1-question')
    d.type('/exit'); d.enter(); time.sleep(4); d.close()
    print('session', sid(log))

def q3():
    d, work, log = start('q3')
    n = count(log, 'Stop')
    prompt(d, 'Spike test of background jobs. Use the Bash tool with run_in_background set to true to run exactly: ./job.sh B 70   '
              + PURPOSE + ' Do not wait for it, do not poll it, do not sleep. After starting it, reply with the single word "started" and end your turn.')
    wait_stop(d, log, n, what='Stop after start')
    s = sid(log)
    d.note(f'session {s}; job B before SIGTERM: {ps_job(work, "B")}')
    import signal
    t0 = time.time()
    os.kill(d.pid, signal.SIGTERM)
    d.wait(lambda: not d.alive, 10, 'claude exit after SIGTERM')
    d.note(f'claude exited after SIGTERM in {time.time()-t0:.1f}s; alive={d.alive}')
    time.sleep(2)
    d.note(f'job B after SIGTERM: {ps_job(work, "B")}')
    open(os.path.join(work, 'q3-ps.txt'), 'w').write(subprocess.run(['ps', '-axo', 'pid,ppid,pgid,stat,command'], capture_output=True, text=True).stdout)
    # resume
    d2, _, log2 = start('q3r', args=('--resume', s), reuse='q3')
    d2.note(f'resumed; job B: {ps_job(work, "B")}')
    n2 = count(log2, 'Stop')
    d2.wait(lambda: os.path.exists(os.path.join(work, 'done-B')), 100, 'job B done file (if it survived)')
    d2.note(f'done-B exists: {os.path.exists(os.path.join(work, "done-B"))}')
    woke = d2.wait(lambda: count(log2, 'Stop') > n2, 60, 'self-woken turn after resume')
    d2.wait_quiet(2, 20)
    dump(d2, 'q3-resumed-after-job')
    n2 = count(log2, 'Stop')
    prompt(d2, 'Without running any tools: is the background job you started earlier still running, finished, or unknown to you? What do you know about its result? One or two lines.')
    wait_stop(d2, log2, n2, what='Stop after question')
    dump(d2, 'q3-question')
    d2.type('/exit'); d2.enter(); time.sleep(4); d2.close()
    print('session', s, 'self-woken after resume', woke)

def q4():
    d, work, log = start('q4')
    n = count(log, 'Stop')
    prompt(d, 'Spike test. Do two things, then end your turn: (1) Use the Monitor tool (load it with ToolSearch if needed) to watch the file '
              'watch.log in this directory, running the command `tail -n0 -F watch.log`, so that each new line becomes a notification to you. '
              '(2) Use CronCreate to schedule a one-shot prompt about 3 minutes from now that says "CRON_FIRED: say cron fired". '
              'Then reply "armed" and end your turn. Do not sleep or poll.')
    wait_stop(d, log, n, timeout=180, what='Stop after arming')
    dump(d, 'q4-armed')
    time.sleep(3)
    d.note('typing /compact'); d.type('/compact'); d.enter()
    d.wait(lambda: count(log, 'PostCompact') > 0, 180, 'PostCompact')
    d.wait_quiet(3, 30)
    n = count(log, 'Stop')
    d.note('appending a line to watch.log')
    open(os.path.join(work, 'watch.log'), 'a').write('MONITOR_LINE_AFTER_COMPACT\n')
    d.wait(lambda: count(log, 'Stop') > n, 60, 'Stop after monitor line')
    d.wait_quiet(3, 20)
    dump(d, 'q4-monitor')
    n = count(log, 'Stop')
    d.wait(lambda: count(log, 'Stop') > n, 240, 'Stop after cron fire')
    d.wait_quiet(3, 20)
    dump(d, 'q4-cron')
    d.type('/exit'); d.enter(); time.sleep(4); d.close()

def q5():
    # What PostToolUse(Monitor) reports, persistent or not, and whether an ended Monitor leaves background_tasks.
    d, work, log = start('q5')
    n = count(log, 'Stop')
    prompt(d, 'Spike test. Use the Monitor tool (load it with ToolSearch if needed) twice, then end your turn: '
              '(1) a persistent monitor running `tail -n0 -F watch.log` with description "watch log"; '
              '(2) a monitor that is not persistent running `sleep 20; echo SHORT_DONE` with description "short". '
              'Then reply "armed" and end your turn. Do not sleep or poll.')
    wait_stop(d, log, n, timeout=180, what='Stop after arming')
    dump(d, 'q5-armed')
    n = count(log, 'Stop')
    d.note('waiting for the short monitor to end and wake the model')
    d.wait(lambda: count(log, 'Stop') > n, 90, 'Stop after the short monitor ends')
    d.wait_quiet(3, 20)
    n = count(log, 'Stop')
    prompt(d, 'Reply with the single word "ok" and end your turn.')
    wait_stop(d, log, n, what='Stop after ok')
    dump(d, 'q5-after')
    for r in recs(log):
        i = r['input']
        if i.get('tool_name') == 'Monitor' and r['ev'] == 'PostToolUse':
            print('PostToolUse(Monitor)', json.dumps(i.get('tool_input')), '->', json.dumps(i.get('tool_response')))
        if r['ev'] == 'Stop':
            print('Stop', r['hms'], json.dumps([(t.get('id'), t.get('type'), t.get('status'), t.get('description')) for t in i.get('background_tasks') or []]))
    d.type('/exit'); d.enter(); time.sleep(4); d.close()

if __name__ == '__main__':
    {'q1': q1, 'q3': q3, 'q4': q4, 'q5': q5}[sys.argv[1]]()
