#!/usr/bin/env python3
"""Spike 12: can a baton-hosted session make its own CLI reliably allowed under auto mode?
V0 no rule (control) · V1 permissions.allow via --settings · V2 autoMode.allow via --settings. Each runs the probe 3x."""
import json, os, shlex, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver
HL = os.path.join(HERE, '..', 'common', 'hooklog.py')
PLUGIN = os.path.join(HERE, '..', '11-plugin-naming', 'plugin')
RULE = ("baton (owner-installed plugin): running its own CLI from the plugin's bin/ directory — `baton …` and "
        "`baton-probe …` — is authorized by the user; it only reads/writes the project's .baton/ state.")
variants = {
    'V0': {},
    'V1': {'permissions': {'allow': ['Bash(baton-probe:*)', 'Bash(baton:*)']}},
    'V2': {'autoMode': {'allow': ['$defaults', RULE]}},
}
for name, extra in variants.items():
    work = os.path.join(HERE, 'work', name); shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
    log = os.path.join(work, 'hooks.log')
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HL)} {ev} --log {shlex.quote(log)}'}]}]
             for ev in ['PreToolUse', 'PostToolUse', 'PermissionRequest', 'PermissionDenied', 'Stop']}
    d = Driver(work, ['claude', '--model', 'sonnet', '--permission-mode', 'auto', '--plugin-dir', PLUGIN,
                      '--settings', json.dumps({'hooks': hooks, **extra})])
    d.wait_quiet(3, 40)
    d.type('Run these three commands with the Bash tool, one call each, even if one is denied: '
           '`baton-probe one`, `baton-probe two`, `baton-probe three`. Then reply DONE.'); d.enter()
    d.wait_log(log, lambda r: r['ev'] == 'Stop', 180, 'Stop')
    d.wait_quiet(2, 20); d.type('/exit'); d.enter(); time.sleep(3); d.close()
    ran = denied = 0
    for l in open(log):
        r = json.loads(l)
        if r['ev'] == 'PostToolUse': ran += 1
        if r['ev'] == 'PermissionDenied': denied += 1
    print(f'{name}: ran={ran} denied={denied}', flush=True)
