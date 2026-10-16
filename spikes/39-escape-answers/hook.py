#!/usr/bin/env python3
"""Spike 39 settings hook: log {ev, input} of every event to the log named by its second argument.

Tool results are kept to 2,000 characters, so what a dismissed question's or a rejected call's result says is
kept whole.
"""
import json, sys, time

ev, log_path = sys.argv[1], sys.argv[2]
raw = sys.stdin.read()
try:
    inp = json.loads(raw)
except Exception:
    inp = {'_raw': raw[:500]}


def shrink(v, n=2000):
    if isinstance(v, str) and len(v) > n:
        return v[:n] + f'…(+{len(v) - n})'
    if isinstance(v, dict):
        return {k: shrink(x, n) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x, n) for x in v[:20]]
    return v


with open(log_path, 'a') as f:
    f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp)}) + '\n')
