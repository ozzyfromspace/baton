#!/usr/bin/env python3
"""Spike 43 settings hook and status line: log {ev, input} to the log named by argv[2]. For `StatusLine` it also
prints a line, as a status line command must."""
import json, sys, time

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


with open(log_path, 'a') as f:
    f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp)}) + '\n')
if ev == 'StatusLine':
    print('spk status')
