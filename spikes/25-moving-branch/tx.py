#!/usr/bin/env python3
"""Print what the model was shown: user prompts, tool results and non-boilerplate attachments. usage: tx.py results/<scn>"""
import json, sys
SKIP = {'total_tokens_reminder', 'prompt_snapshot', 'skill_listing', 'deferred_tools_delta', 'agent_listing_delta',
        'mcp_instructions_delta', 'environment', 'model', 'session_context', 'date', 'credential_org',
        'remote_session_change', 'deferred_tools_record', 'hook_success'}
for l in open(sys.argv[1] + '/transcript.jsonl'):
    e = json.loads(l); t = e.get('type'); ts = e.get('timestamp', '')[11:23]
    if t == 'attachment':
        a = e['attachment']
        if a.get('type') not in SKIP: print(ts, 'ATTACHMENT', a.get('type'), json.dumps(a)[:1500])
    elif t == 'user':
        c = e['message']['content']
        for b in (c if isinstance(c, list) else [{'type': 'text', 'text': c}]):
            if b.get('type') == 'tool_result': print(ts, 'TOOL_RESULT', json.dumps(b.get('content'))[:1200])
            else: print(ts, 'USER', json.dumps(b)[:600])
    elif t == 'assistant':
        for b in e['message'].get('content', []):
            if b.get('type') == 'tool_use': print(ts, 'TOOL_USE', b['name'], json.dumps(b['input'])[:300])
            elif b.get('type') == 'text': print(ts, 'ASSISTANT', b['text'][:600].replace('\n', ' / '))
