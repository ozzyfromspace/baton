#!/usr/bin/env python3
"""Spike 40 settings hook: log {ev, input} to the log named by argv[2]. At UserPromptSubmit, a prompt that starts with
`@<name>` and whitespace is kept from the model, as baton's lead hook would keep it for a phase agent, in one of three
shapes chosen by a token in the words: plain {"decision": "block", "reason"}; `[sys]` adds a "systemMessage";
`[cont]` answers {"continue": false, "stopReason"} instead.
"""
import json, re, sys, time

ev, log_path = sys.argv[1], sys.argv[2]
raw = sys.stdin.read()
try:
    inp = json.loads(raw)
except Exception:
    inp = {'_raw': raw[:500]}


def shrink(v):
    if isinstance(v, str) and len(v) > 300:
        return v[:300] + f'…(+{len(v) - 300})'
    if isinstance(v, dict):
        return {k: shrink(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x) for x in v[:10]]
    return v


MENTION = re.compile(r'^@([A-Za-z0-9][A-Za-z0-9_-]*)\s+(.+)$', re.S)
out = None
if ev == 'UserPromptSubmit':
    m = MENTION.match(inp.get('prompt') or '')
    if m:
        name, words = m.group(1), m.group(2).strip()
        reason = f'spk: sent to {name}: "{words}"'
        if '[cont]' in words:
            out = {'continue': False, 'stopReason': reason}
        else:
            out = {'decision': 'block', 'reason': reason}
            if '[sys]' in words:
                out['systemMessage'] = 'spk sys: ' + reason

with open(log_path, 'a') as f:
    f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp), 'out': out}) + '\n')
if out:
    print(json.dumps(out))
