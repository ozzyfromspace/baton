#!/usr/bin/env python3
"""Spike: plan approval hooks and session identity (Claude Code 2.1.292, Haiku). Usage: run.py <scenario>"""
import json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HL = os.path.join(HERE, 'hooklog.py')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'PermissionDenied', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact',
          'SubagentStart', 'SubagentStop']

def setup(name, extra=None, reuse=None):
    work = os.path.join(HERE, 'work', reuse or name)
    if not reuse:
        shutil.rmtree(work, ignore_errors=True)
        os.makedirs(work)
        subprocess.run(['git', 'init', '-q'], cwd=work)
    log = os.path.join(work, 'hooks.log' if not reuse else f'hooks-{name}.log')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HL)} {ev} --log {shlex.quote(log)}'}]}] for ev in EVENTS}
    s = {'hooks': hooks, 'permissions': {'allow': ['Bash(echo:*)']}}
    s.update(extra or {})
    return work, log, json.dumps(s)

def recs(log):
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []

def count(log, ev, pred=lambda r: True):
    return sum(1 for r in recs(log) if r['ev'] == ev and pred(r))

def tool(name):
    return lambda r: r['input'].get('tool_name') == name

def dump_screen(d, label, n=2500):
    with d.lock:
        t = d.tail[-n:].decode('utf-8', 'replace')
    with open(os.path.join(d.cwd, f'screen-{label}.txt'), 'w') as f:
        f.write(t)
    d.note(f'screen dumped: {label}')

def plan_mode(d):
    for _ in range(5):
        if d.screen_has('plan mode on'):
            return True
        d.key(b'\x1b[Z'); time.sleep(1.2)
    return d.screen_has('plan mode on')

def prompt(d, text):
    d.type(text); d.enter()

PLAN_ASK = ('Make a plan for this tiny task: create a file {a} containing "one" (phase P0), then a file {b} containing "two" (phase P1). '
            'Write the plan to your plan file with exactly these headings: "## P0 — Create {a}" and "## P1 — Create {b}". '
            'Do not explore the codebase. Then call ExitPlanMode.')

def wait_exitplan_dialog(d, log, n_before, timeout=150):
    ok = d.wait(lambda: count(log, 'PreToolUse', tool('ExitPlanMode')) > n_before, timeout, 'ExitPlanMode PreToolUse')
    time.sleep(3); d.wait_quiet(1.5, 20)
    return ok

def start(name, extra=None, args=(), reuse=None):
    work, log, settings = setup(name, extra, reuse)
    d = Driver(work, ['claude', '--model', 'haiku', '--settings', settings, *args])
    d.trust(25)
    d.wait_quiet(3, 40)
    return d, work, log

def finish(d):
    d.type('/exit'); d.enter(); time.sleep(4); d.close()

def scenario_A():
    """S2 reject, S1 approve, S3 /clear + env + replan."""
    d, work, log = start('A')
    d.note('plan mode'); plan_mode(d); dump_screen(d, 'planmode')
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    prompt(d, PLAN_ASK.format(a='a.txt', b='b.txt'))
    wait_exitplan_dialog(d, log, n); dump_screen(d, 'dialog1')
    # S2: reject with "No, keep planning" — find it on screen
    d.note('rejecting: pressing 3? check screen');
    scr = open(os.path.join(work, 'screen-dialog1.txt')).read()
    opt = None
    for line in scr.splitlines():
        if 'keep planning' in line.lower() or 'no,' in line.lower():
            for ch in line:
                if ch.isdigit():
                    opt = ch; break
            if opt: break
    d.note(f'reject option digit: {opt}')
    if opt:
        d.key(opt.encode())
    else:
        d.key(b'\x1b')
    time.sleep(2); dump_screen(d, 'after-reject-key')
    d.type('Please change the second file name to c.txt and call ExitPlanMode again.'); d.enter()
    time.sleep(3)
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    # maybe the feedback ended the turn; wait for either a new ExitPlanMode or a Stop
    ok = d.wait(lambda: count(log, 'PreToolUse', tool('ExitPlanMode')) > n, 120, 'second ExitPlanMode')
    if not ok:
        dump_screen(d, 'no-second-exit')
        prompt(d, 'Call ExitPlanMode now with the updated plan.')
        d.wait(lambda: count(log, 'PreToolUse', tool('ExitPlanMode')) > n, 120, 'second ExitPlanMode (retry)')
    time.sleep(3); d.wait_quiet(1.5, 20); dump_screen(d, 'dialog2')
    d.note('approving with Enter'); d.key(b'\r')
    d.wait(lambda: count(log, 'PostToolUse', tool('ExitPlanMode')) > 0, 30, 'PostToolUse ExitPlanMode')
    n = count(log, 'Stop')
    d.wait(lambda: count(log, 'Stop') > n, 180, 'Stop after implementation')
    d.wait_quiet(2, 30); dump_screen(d, 'after-impl')
    # S3: /clear
    d.note('/clear'); d.type('/clear'); d.enter(); time.sleep(5); d.wait_quiet(2, 30)
    n = count(log, 'Stop')
    prompt(d, 'Run this exact Bash command and reply with its output: echo "SID=$CLAUDE_CODE_SESSION_ID"')
    d.wait(lambda: count(log, 'Stop') > n, 90, 'Stop after echo')
    d.wait_quiet(2, 20)
    d.note('plan mode after clear'); plan_mode(d)
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    prompt(d, PLAN_ASK.format(a='d.txt', b='e.txt'))
    wait_exitplan_dialog(d, log, n); dump_screen(d, 'dialog3')
    d.key(b'\x1b'); time.sleep(2); dump_screen(d, 'after-esc3')
    d.wait_quiet(2, 20)
    finish(d)

def scenario_B(resume_id):
    """S5: resume the A session (pre-clear id) and plan again."""
    d, work, log = start('B', args=('--resume', resume_id), reuse='A')
    d.note('plan mode'); plan_mode(d)
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    prompt(d, PLAN_ASK.format(a='f.txt', b='g.txt'))
    wait_exitplan_dialog(d, log, n); dump_screen(d, 'dialog')
    d.key(b'\x1b'); time.sleep(2); d.wait_quiet(2, 20)
    finish(d)

def scenario_C():
    """S4: approve with the clear-context option."""
    d, work, log = start('C', extra={'showClearContextOnPlanAccept': True})
    d.note('plan mode'); plan_mode(d)
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    prompt(d, PLAN_ASK.format(a='a.txt', b='b.txt'))
    wait_exitplan_dialog(d, log, n); dump_screen(d, 'dialog0')
    scr = open(os.path.join(work, 'screen-dialog0.txt')).read()
    fb = None
    for line in scr.splitlines():
        if 'whattochange' in line.replace(' ', '').lower():
            for ch in line:
                if ch.isdigit():
                    fb = ch; break
            if fb: break
    d.note(f'feedback option digit: {fb}')
    n = count(log, 'PreToolUse', tool('ExitPlanMode'))
    if fb:
        d.key(fb.encode()); time.sleep(1.5); dump_screen(d, 'after-fb-key')
        d.type('Rename b.txt to c.txt in the plan, then call ExitPlanMode again.'); d.enter()
        time.sleep(2); dump_screen(d, 'after-fb-enter')
    wait_exitplan_dialog(d, log, n); dump_screen(d, 'dialog')
    scr = open(os.path.join(work, 'screen-dialog.txt')).read()
    opt = None
    for line in scr.splitlines():
        if 'clearcontext' in line.replace(' ', '').lower():
            for ch in line:
                if ch.isdigit():
                    opt = ch; break
            if opt: break
    d.note(f'clear-context option digit: {opt}')
    if not opt:
        d.close(); return
    d.key(opt.encode())
    time.sleep(3); dump_screen(d, 'after-choose')
    n = count(log, 'Stop')
    d.wait(lambda: count(log, 'Stop') > n, 180, 'Stop after clear-context implementation')
    d.wait_quiet(3, 30); dump_screen(d, 'after-impl')
    finish(d)

def scenario_D():
    """S6: two concurrent sessions in one dir."""
    work, log, settings = setup('D')
    d1 = Driver(work, ['claude', '--model', 'haiku', '--settings', settings])
    d1.trust(25); d1.wait_quiet(3, 40)
    d1.screen_path  # noqa
    # second driver writes its own screen log
    os.makedirs(os.path.join(work, 'd2'), exist_ok=True)
    d2 = Driver(work, ['claude', '--model', 'haiku', '--settings', settings])
    d2.screen = open(os.path.join(work, 'screen2.log'), 'wb')
    d2.trust(25); d2.wait_quiet(3, 40)
    for d, (a, b) in ((d1, ('h.txt', 'i.txt')), (d2, ('j.txt', 'k.txt'))):
        plan_mode(d)
        prompt(d, PLAN_ASK.format(a=a, b=b))
    d1.wait(lambda: count(log, 'PreToolUse', tool('ExitPlanMode')) >= 2, 180, 'both ExitPlanMode')
    time.sleep(3)
    for d in (d1, d2):
        d.key(b'\x1b'); time.sleep(1)
    time.sleep(3)
    for d in (d1, d2):
        finish(d)

if __name__ == '__main__':
    which = sys.argv[1]
    if which == 'A': scenario_A()
    elif which == 'B': scenario_B(sys.argv[2])
    elif which == 'C': scenario_C()
    elif which == 'D': scenario_D()
    print('done', which)
