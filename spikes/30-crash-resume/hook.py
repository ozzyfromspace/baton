#!/usr/bin/env python3
"""Spike hook: log {ev, input} plus what the transcript holds at that moment (its last entries' type,
uuid and parent), so a rewind's trace and the order of hook and transcript write can be read."""
import json, os, sys, time
ev = sys.argv[1]; log_path = sys.argv[2]
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

def brief(d):
    m = d.get('message') or {}
    c = m.get('content')
    text = c if isinstance(c, str) else ''
    if isinstance(c, list):
        kinds = [x.get('type') for x in c if isinstance(x, dict)]
        text = ','.join(kinds)
        for x in c:
            if isinstance(x, dict) and x.get('type') == 'text':
                text += ':' + x.get('text', '')[:40]
                break
    return {'type': d.get('type'), 'sub': d.get('subtype'), 'uuid': (d.get('uuid') or '')[:8],
            'parent': (d.get('parentUuid') or '')[:8], 'leaf': (d.get('leafUuid') or '')[:8], 'text': text[:60]}

tail = None
tp = inp.get('transcript_path')
if tp:
    try:
        lines = [json.loads(l) for l in open(tp) if l.strip()]
        tail = {'n': len(lines), 'last': [brief(d) for d in lines[-4:]]}
    except Exception as e:
        tail = {'error': str(e)}
rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp), 'transcript': tail}
with open(log_path, 'a') as f:
    f.write(json.dumps(rec) + '\n')
# A PreToolUse with work/hold present holds its tool call, so the driver can kill claude mid-turn.
if ev == 'PreToolUse' and os.path.exists(os.path.join(inp.get('cwd') or '.', 'hold')):
    time.sleep(90)
