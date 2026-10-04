#!/usr/bin/env python3
"""Spike 10: what reaches Claude Code when baton types?
  A) which key sequence reliably empties a non-empty input box (single- and multi-line drafts);
  B) /compact typed while the model is busy;
  C) dialog signals: permission prompt and AskUserQuestion;
  D) autocomplete clash: typing /compact when a /compactor command exists.
Every submission is observed through UserPromptSubmit / PreCompact hook logs."""
import json, os, shlex, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HL = os.path.join(HERE, '..', 'common', 'hooklog.py')
EVENTS = ['UserPromptSubmit', 'UserPromptExpansion', 'PreCompact', 'PostCompact', 'Notification', 'PermissionRequest',
          'PreToolUse', 'PostToolUse', 'Stop', 'SessionEnd']

def setup(name, compactor=False):
    work = os.path.join(HERE, 'work', name); shutil.rmtree(work, ignore_errors=True); os.makedirs(os.path.join(work, '.claude', 'commands'))
    log = os.path.join(work, 'hooks.log')
    json.dump({'permissions': {'allow': ['Bash(echo:*)', 'Bash(sleep:*)']}}, open(os.path.join(work, '.claude', 'settings.json'), 'w'))
    if compactor:
        open(os.path.join(work, '.claude', 'commands', 'compactor.md'), 'w').write('---\ndescription: test command that shares a prefix with /compact\n---\nReply with exactly: COMPACTOR-RAN\n')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HL)} {ev} --log {shlex.quote(log)}'}]}] for ev in EVENTS}
    return work, log, json.dumps({'hooks': hooks})

def stops(log):
    try:
        return sum(1 for l in open(log) if json.loads(l)['ev'] == 'Stop')
    except FileNotFoundError:
        return 0

def turn(d, log, send):
    """Run one human turn: send() types it; wait until the next Stop."""
    n = stops(log); send()
    d.wait(lambda: stops(log) > n, 90, 'Stop after turn'); d.wait_quiet(1.5, 20)

def prompt(d, text):
    d.type(text); d.enter()

# ---------------- session A
work, log, settings = setup('A')
d = Driver(work, ['claude', '--model', 'haiku', '--settings', settings])
d.wait_quiet(3, 40)
turn(d, log, lambda: prompt(d, 'Say hi in two words.'))

d.note('A1 single-line draft, Ctrl-U, PROBE1')
turn(d, log, lambda: (d.type('aaa bbb ccc'), time.sleep(0.5), d.key(b'\x15'), time.sleep(0.3), prompt(d, 'PROBE1 reply ok')))

d.note('A2 two-line draft (backslash-enter), Ctrl-U once, PROBE2')
turn(d, log, lambda: (d.type('line one\\'), d.key(b'\r'), time.sleep(0.3), d.type('line two'), time.sleep(0.5),
                      d.key(b'\x15'), time.sleep(0.3), prompt(d, 'PROBE2 reply ok')))

d.note('A3 two-line draft, Escape, PROBE3')
turn(d, log, lambda: (d.type('line one\\'), d.key(b'\r'), time.sleep(0.3), d.type('line two'), time.sleep(0.5),
                      d.key(b'\x1b'), time.sleep(0.6), prompt(d, 'PROBE3 reply ok')))

d.note('A4 two-line draft, Ctrl-U x4, PROBE4')
turn(d, log, lambda: (d.type('line one\\'), d.key(b'\r'), time.sleep(0.3), d.type('line two'), time.sleep(0.5),
                      [d.key(b'\x15') or time.sleep(0.15) for _ in range(4)], time.sleep(0.3), prompt(d, 'PROBE4 reply ok')))

d.note('B /compact typed while the model is busy (sleep 8)')
n = stops(log); prompt(d, 'Run `sleep 8` with the Bash tool, then reply with the word slept.')
d.wait_log(log, lambda r: r['ev'] == 'PreToolUse' and 'sleep' in json.dumps(r['input'].get('tool_input', {})), 60, 'sleep started')
time.sleep(2); d.note('typing /compact while busy'); d.type('/compact'); d.enter()
d.wait(lambda: stops(log) > n, 120, 'Stop after busy turn'); time.sleep(25); d.wait_quiet(2, 60)

d.note('C1 permission prompt (touch is not allowed)')
n = stops(log); prompt(d, 'Run `touch perm_probe.txt` with the Bash tool.')
d.wait_log(log, lambda r: r['ev'] in ('PermissionRequest', 'Notification'), 60, 'permission signal')
time.sleep(3); d.note('approving with Enter'); d.key(b'\r')
d.wait(lambda: stops(log) > n, 90, 'Stop after permission turn'); d.wait_quiet(1.5, 20)

d.note('C2 AskUserQuestion dialog')
n = stops(log); prompt(d, 'Use the AskUserQuestion tool to ask me to pick a color: red or blue.')
d.wait_log(log, lambda r: r['ev'] == 'PreToolUse' and r['input'].get('tool_name') == 'AskUserQuestion', 60, 'AskUserQuestion shown')
time.sleep(4); d.note('answering first option with Enter'); d.key(b'\r')
d.wait(lambda: stops(log) > n, 90, 'Stop after question turn'); d.wait_quiet(1.5, 20)
d.type('/exit'); d.enter(); time.sleep(3); d.close()

# ---------------- session B: autocomplete clash
work, log, settings = setup('B', compactor=True)
d = Driver(work, ['claude', '--model', 'haiku', '--settings', settings])
d.wait_quiet(3, 40)
turn(d, log, lambda: prompt(d, 'Say hi in two words.'))
d.note('D typing /compact with /compactor present')
d.type('/compact'); d.enter()
d.wait_log(log, lambda r: r['ev'] in ('PreCompact', 'UserPromptSubmit', 'UserPromptExpansion') and 'Say hi' not in json.dumps(r['input']), 60, 'compact or compactor')
time.sleep(20); d.wait_quiet(2, 40); d.type('/exit'); d.enter(); time.sleep(3); d.close()
print('done')
