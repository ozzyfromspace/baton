#!/usr/bin/env python3
"""Spike hook: append {ev, input} as one JSON line to --log. If --announce FILE exists, deliver its
text once as additionalContext (UserPromptSubmit / PostToolUse), the way baton would tell the lead
that work landed under it, then rename it to FILE.delivered.
usage: hooklog.py <Event> --log PATH [--announce FILE]"""
import json, os, sys, time
args = sys.argv[1:]; ev = args.pop(0); opt = {}
while args:
    k = args.pop(0); opt[k] = args.pop(0)
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
out = None
ann = opt.get('--announce')
if ann and ev in ('UserPromptSubmit', 'PostToolUse') and os.path.exists(ann):
    try:
        text = open(ann).read().strip()
        os.rename(ann, ann + '.delivered')
        out = {'systemMessage': 'baton: P3 landed on run (see context)',
               'hookSpecificOutput': {'hookEventName': ev, 'additionalContext': text}}
    except Exception:
        out = None
rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'emitted': out, 'input': shrink(inp)}
with open(opt['--log'], 'a') as f:
    f.write(json.dumps(rec) + '\n')
if out:
    print(json.dumps(out))
