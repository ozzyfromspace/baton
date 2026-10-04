#!/usr/bin/env python3
"""Spike 2 hook: sync Stop lowers the window; async-rewake Stop wakes the model; post-compact restores + briefs."""
import json, os, sys, time, fcntl
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LOG, STATE = os.path.join(ROOT, 'log.jsonl'), os.path.join(ROOT, 'state.json')
LOCAL = os.path.join(ROOT, '.claude', 'settings.local.json')
WAIT = float(os.environ.get('REWAKE_WAIT', '6'))
ev = sys.argv[1]
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}

def load(p, d):
    try:
        return json.load(open(p))
    except Exception:
        return d

def save(p, d):
    tmp = p + '.tmp'
    json.dump(d, open(tmp, 'w'), indent=1); os.replace(tmp, p)

def log(**kw):
    with open(LOG, 'a') as f:
        f.write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, **kw}) + '\n')

def locked(fn):
    with open(STATE + '.lock', 'w') as lk:
        fcntl.flock(lk, fcntl.LOCK_EX)
        st = load(STATE, {'p1_done': False, 'lowered': False, 'rewoken': False, 'compacted': False, 'p2_done': False})
        out = fn(st)
        save(STATE, st)
        return out

keys = {k: inp[k] for k in ('source', 'trigger', 'stop_hook_active') if k in inp}

if ev == 'PostToolUse':
    cmd = (inp.get('tool_input') or {}).get('command', '')
    def f(st):
        if 'PHASE1_DONE' in cmd: st['p1_done'] = True
        if 'PHASE2_DONE' in cmd: st['p2_done'] = True
    locked(f); log(cmd=cmd)
elif ev == 'Stop':
    def f(st):
        if st['p1_done'] and not st['lowered']:
            s = load(LOCAL, {}); s['autoCompactWindow'] = 100000; save(LOCAL, s)
            st['lowered'] = True
            return 'lowered window to 100k, allowing stop'
        return 'allow stop'
    log(action=locked(f), **keys)
elif ev == 'StopRewake':
    time.sleep(WAIT)
    def f(st):
        if st['lowered'] and not st['rewoken'] and not st['compacted']:
            st['rewoken'] = True
            return True
        return False
    if locked(f):
        log(action=f'rewaking model after {WAIT}s')
        sys.stderr.write('smart-compact: Phase 1 is recorded complete. Continue the plan.\n')
        sys.exit(2)
    log(action='no rewake')
elif ev == 'SessionStart' and inp.get('source') == 'compact':
    s = load(LOCAL, {}); s.pop('autoCompactWindow', None); save(LOCAL, s)
    locked(lambda st: st.__setitem__('compacted', True))
    log(action='restored window, injecting brief', **keys)
    brief = ('SMART-COMPACT BRIEF (injected after compaction): Phase 1 is complete. You are now on Phase 2. '
             'Phase 2 steps: (a) run `echo PHASE2_START`; (b) run `echo PHASE2_DONE PINEAPPLE`; '
             '(c) end your turn with one line stating the codeword from this brief.')
    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': brief}}))
else:
    summ = inp.get('compact_summary')
    log(summary_chars=len(summ) if isinstance(summ, str) else None, **keys)
