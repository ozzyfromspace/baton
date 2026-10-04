#!/usr/bin/env python3
"""Spike 11: plugin `baton` + skill `baton` loaded with --plugin-dir, Sonnet in auto mode.
Is it /baton or /baton:baton? Do arguments arrive? Is the plugin's bin/ on the Bash PATH? Does auto mode allow it?"""
import json, os, shlex, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver
HL = os.path.join(HERE, '..', 'common', 'hooklog.py')
work = os.path.join(HERE, 'work'); shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
log = os.path.join(work, 'hooks.log')
hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HL)} {ev} --log {shlex.quote(log)}'}]}]
         for ev in ['UserPromptSubmit', 'UserPromptExpansion', 'PreToolUse', 'PostToolUse', 'PermissionRequest', 'PermissionDenied', 'Stop']}
d = Driver(work, ['claude', '--model', 'sonnet', '--permission-mode', 'auto', '--plugin-dir', os.path.join(HERE, 'plugin'),
                  '--settings', json.dumps({'hooks': hooks})])
d.wait_quiet(3, 40)
def stops():
    try: return sum(1 for l in open(log) if json.loads(l)['ev'] == 'Stop')
    except FileNotFoundError: return 0
for cmd in ['/baton hello world', '/baton:baton second try']:
    n = stops(); d.note(f'typing {cmd}'); d.type(cmd); d.enter()
    d.wait(lambda: stops() > n, 120, f'Stop after {cmd}'); d.wait_quiet(2, 30)
d.type('/exit'); d.enter(); time.sleep(3); d.close(); print('done')
