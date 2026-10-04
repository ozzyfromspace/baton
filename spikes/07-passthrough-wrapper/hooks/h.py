#!/usr/bin/env python3
"""Spike 7 hooks. Dormant unless running under the wrapper (SC_DIR set)."""
import json, os, sys, time
D = os.environ.get('SC_DIR')
if not D:
    sys.exit(0)
ev = sys.argv[1]
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}
STATE = os.path.join(D, 'state.json')
st = json.load(open(STATE)) if os.path.exists(STATE) else {'p1_done': False, 'requested': False, 'p2_done': False}

def save():
    json.dump(st, open(STATE, 'w'))

def log(**kw):
    with open(os.path.join(D, 'hooks.log'), 'a') as f:
        f.write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, **kw}) + '\n')

if ev == 'PostToolUse':
    cmd = (inp.get('tool_input') or {}).get('command', '')
    if 'PHASE1_DONE' in cmd: st['p1_done'] = True
    if 'PHASE2_DONE' in cmd: st['p2_done'] = True
    save(); log(cmd=cmd)
elif ev == 'Stop':
    if st['p1_done'] and not st['requested']:
        st['requested'] = True; save()
        open(os.path.join(D, 'compact.request'), 'w').write('phase 1 done\n')
        log(action='requested compaction')
        print(json.dumps({'systemMessage': 'smart-compact (spike): Phase 1 recorded done. Compacting once the screen is idle.'}))
    else:
        log(stop_hook_active=inp.get('stop_hook_active'))
elif ev == 'PreCompact':
    open(os.path.join(D, 'compact.ack'), 'w').write(inp.get('trigger', '?'))
    log(trigger=inp.get('trigger'))
    print(json.dumps({'systemMessage': f"smart-compact (spike): compaction started ({inp.get('trigger')})."}))
elif ev == 'SessionStart' and inp.get('source') == 'compact':
    log(action='brief')
    brief = ('SMART-COMPACT BRIEF (injected after compaction): Phase 1 is complete. You are now on Phase 2. '
             'Phase 2 steps: (a) run `echo PHASE2_START`; (b) run `echo PHASE2_DONE PINEAPPLE`; '
             '(c) end your turn with one line stating the codeword from this brief.')
    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': brief}}))
elif ev == 'PostCompactRewake':
    log(action='rewake in 2s')
    time.sleep(2)
    sys.stderr.write('smart-compact: compaction finished. Continue the plan from the brief now.\n')
    sys.exit(2)
