#!/usr/bin/env python3
"""Print a --settings JSON that logs every baton-relevant hook event to LOG. usage: mksettings.py LOG [extra-json]"""
import json, shlex, sys, os
HERE = os.path.dirname(os.path.abspath(__file__))
log = sys.argv[1]
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'UserPromptExpansion', 'PreToolUse', 'PostToolUse',
          'PostToolUseFailure', 'PermissionRequest', 'PermissionDenied', 'Notification', 'Stop', 'StopFailure',
          'PreCompact', 'PostCompact', 'CwdChanged', 'SubagentStart', 'SubagentStop']
cmd = lambda ev: f'python3 {shlex.quote(os.path.join(HERE, "hooklog.py"))} {ev} {shlex.quote(log)}'
s = {'hooks': {ev: [{'hooks': [{'type': 'command', 'command': cmd(ev)}]}] for ev in EVENTS}}
if len(sys.argv) > 2: s.update(json.loads(sys.argv[2]))
print(json.dumps(s))
