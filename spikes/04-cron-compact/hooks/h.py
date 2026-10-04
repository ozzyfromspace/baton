#!/usr/bin/env python3
"""Spike 4 hook: does a cron-scheduled `/compact` run as a command, and does a PostCompact asyncRewake resume the model?"""
import json, os, sys, time
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LOG, STATE = os.path.join(ROOT, 'log.jsonl'), os.path.join(ROOT, 'state.json')
ev = sys.argv[1]
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}

def load():
    try:
        return json.load(open(STATE))
    except Exception:
        return {'p1_done': False, 'compacted': False, 'p2_done': False}

def save(st):
    json.dump(st, open(STATE, 'w'))

def log(**kw):
    with open(LOG, 'a') as f:
        f.write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, **kw}) + '\n')

st = load()
keys = {k: inp[k] for k in ('source', 'trigger', 'stop_hook_active', 'custom_instructions') if k in inp}
if ev == 'PostToolUse':
    ti = inp.get('tool_input') or {}
    cmd = ti.get('command', '')
    if 'PHASE1_DONE' in cmd: st['p1_done'] = True
    if 'PHASE2_DONE' in cmd: st['p2_done'] = True
    save(st); log(tool=inp.get('tool_name'), input=json.dumps(ti)[:160])
elif ev == 'SessionStart' and inp.get('source') == 'compact':
    st['compacted'] = True; save(st)
    log(action='injecting brief', **keys)
    brief = ('SMART-COMPACT BRIEF (injected after compaction): Phase 1 is complete. You are now on Phase 2. '
             'Phase 2 steps: (a) run `echo PHASE2_START`; (b) run `echo PHASE2_DONE PINEAPPLE`; '
             '(c) end your turn with one line stating the codeword from this brief.')
    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': brief}}))
elif ev == 'PostCompactRewake':
    s = inp.get('compact_summary')
    log(action='post-compact; sleeping 3s then rewaking', summary_chars=len(s) if isinstance(s, str) else None, **keys)
    time.sleep(3)
    sys.stderr.write('smart-compact: compaction finished. Continue the plan from the brief now.\n')
    sys.exit(2)
else:
    log(**keys)
