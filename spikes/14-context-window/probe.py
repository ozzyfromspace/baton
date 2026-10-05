#!/usr/bin/env python3
"""Spike 14: what does the status line input report about the context under --autocompact?

    python3 probe.py haiku 100k haiku100k
    python3 probe.py 'opus[1m]' 810k opus810k

Logs every status line input to work-<tag>/statusline.jsonl and prints each distinct context_window."""
import json, os, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

model, cap, tag = sys.argv[1], sys.argv[2], sys.argv[3]
work = os.path.join(HERE, 'work-' + tag); shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
log = os.path.join(work, 'statusline.jsonl')
settings = {'statusLine': {'type': 'command', 'command': f'cat >> "{log}"; echo >> "{log}"; echo probe'}}
argv = ['claude', '--model', model, '--settings', json.dumps(settings)]
if cap != 'none':
    argv += ['--autocompact', cap]
argv += ['Run `echo hi` with the Bash tool, then reply with the single word ok.']
d = Driver(work, argv)
d.trust()

def lines():
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except Exception:
        return []

def answered():
    return any((r.get('context_window') or {}).get('used_percentage') for r in lines())

d.wait(answered, 120, 'status line with usage')
time.sleep(8); d.wait_quiet(2, 30)
d.type('/exit'); d.enter(); time.sleep(3); d.close()
seen = set()
for r in lines():
    cw = json.dumps(r.get('context_window'), sort_keys=True)
    if cw not in seen:
        seen.add(cw); print(tag, (r.get('model') or {}).get('id'), cw)
