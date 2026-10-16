#!/usr/bin/env python3
"""Spike 39: one scenario's hooks in time order, with the driver's steps among them.

Usage: digest.py <workdir>
"""
import json, os, sys

work = sys.argv[1]
for line in open(os.path.join(work, 'hooks.jsonl')):
    r = json.loads(line)
    i = r.get('input', {})
    if r['ev'] == 'DRIVER':
        print(r['hms'], '==', i.get('step', '')[:140])
        continue
    row = {k: i.get(k) for k in ('tool_name', 'agent_id', 'agent_type', 'notification_type', 'message', 'permission_mode', 'reason',
                                 'error', 'is_interrupt') if i.get(k) not in (None, '')}
    if row.get('agent_id'):
        row['agent_id'] = row['agent_id'][:8]
    if i.get('tool_name') == 'Bash':
        row['command'] = (i.get('tool_input') or {}).get('command')
    if r['ev'] == 'UserPromptSubmit':
        row['prompt'] = (i.get('prompt') or '')[:100]
    if r['ev'] in ('Stop', 'SubagentStop'):
        row['last'] = (i.get('last_assistant_message') or '')[:140]
    if r['ev'] in ('PostToolUse', 'PostToolUseFailure', 'PermissionDenied') and i.get('tool_name') != 'AskUserQuestion':
        row['response'] = json.dumps(i.get('tool_response'))[:160]
    print(r['hms'], r['ev'], json.dumps(row))
