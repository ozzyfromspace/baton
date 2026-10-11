#!/usr/bin/env python3
# Spike hook: log every input; for PreToolUse(Bash) act per MODE.
import json, os, sys, time
d = json.load(sys.stdin)
mode = os.environ.get('MODE', 'log')
log = os.environ['HOOKLOG']
out = None
ev = d.get('hook_event_name')
if ev == 'PreToolUse' and d.get('tool_name') == 'Bash':
    ti = dict(d.get('tool_input') or {})
    cmd = ti.get('command', '')
    if mode == 'bg' and 'SPIKE_FG' in cmd and not ti.get('run_in_background'):
        ti['run_in_background'] = True
        out = {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": ti}}
    elif mode == 'bg-ctx' and 'SPIKE_FG' in cmd and not ti.get('run_in_background'):
        ti['run_in_background'] = True
        out = {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": ti,
               "additionalContext": "baton moved this command to the background (it looks long-running). You will be notified when it finishes; continue with other work meanwhile."}}
    elif mode == 'wrap' and 'ORIGINAL' in cmd:
        ti['command'] = cmd.replace('ORIGINAL', 'WRAPPED') + '; echo HOOK_WRAPPED_IT'
        out = {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": ti}}
    elif mode == 'wrap-pass' and 'ORIGINAL' in cmd:
        ti['command'] = cmd.replace('ORIGINAL', 'WRAPPED') + '; echo HOOK_WRAPPED_IT'
        out = {"hookSpecificOutput": {"hookEventName": "PreToolUse", "updatedInput": ti}}
    elif mode == 'deny' and 'SPIKE_FG' in cmd and not ti.get('run_in_background'):
        out = {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny",
               "permissionDecisionReason": "baton: long commands must not run in the foreground. Run this exact command again with run_in_background: true, then continue with other work."}}
with open(log, 'a') as f:
    f.write(json.dumps({"t": time.time(), "mode": mode, "event": ev, "input": d, "output": out}) + "\n")
if out: print(json.dumps(out))
sys.exit(0)
