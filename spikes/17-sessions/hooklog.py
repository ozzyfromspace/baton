#!/usr/bin/env python3
"""Spike hook: append {ev, env, input} as one JSON line to the log given by --log."""
import json, os, sys, time
ev = sys.argv[1]; log_path = sys.argv[3]
raw = sys.stdin.read()
try:
    inp = json.loads(raw)
except Exception:
    inp = {'_raw': raw[:500]}
def shrink(v):
    if isinstance(v, str) and len(v) > 400:
        return v[:400] + f'…(+{len(v) - 400})'
    if isinstance(v, dict):
        return {k: shrink(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x) for x in v[:10]]
    return v
rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev,
       'env_sid': os.environ.get('CLAUDE_CODE_SESSION_ID'), 'input': shrink(inp)}
with open(log_path, 'a') as f:
    f.write(json.dumps(rec) + '\n')
