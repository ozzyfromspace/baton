#!/usr/bin/env python3
"""Spike 45 settings hook: log {ev, input} to the log named by argv[2], each string clipped to 2000 characters, so a
tool_response is kept whole."""
import json, sys, time

ev, log_path = sys.argv[1], sys.argv[2]
raw = sys.stdin.read()
try:
    inp = json.loads(raw)
except Exception:
    inp = {'_raw': raw[:500]}


def shrink(v):
    if isinstance(v, str) and len(v) > 2000:
        return v[:2000] + f'…(+{len(v) - 2000})'
    if isinstance(v, dict):
        return {k: shrink(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x) for x in v[:20]]
    return v


with open(log_path, 'a') as f:
    f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp)}) + '\n')
