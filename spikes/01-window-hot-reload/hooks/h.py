#!/usr/bin/env python3
"""Spike hook: one script for every event (argv[1]). Logs to log.jsonl, keeps state in state.json."""
import json, os, sys, time
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LOG, STATE = os.path.join(ROOT, 'log.jsonl'), os.path.join(ROOT, 'state.json')
LOCAL = os.path.join(ROOT, '.claude', 'settings.local.json')
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
    json.dump(d, open(p, 'w'), indent=1)

def ctx(tp):
    last = None
    try:
        for line in open(tp):
            try:
                e = json.loads(line)
            except Exception:
                continue
            if e.get('type') == 'assistant' and (e.get('message') or {}).get('usage'):
                last = e['message']['usage']
    except Exception:
        return None
    if not last:
        return None
    return last.get('input_tokens', 0) + last.get('cache_creation_input_tokens', 0) + last.get('cache_read_input_tokens', 0)

def log(**kw):
    with open(LOG, 'a') as f:
        f.write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, **kw}) + '\n')

st = load(STATE, {'p1_done': False, 'lowered': False, 'compacted': False, 'p2_done': False, 'stops': 0})
keys = {k: inp[k] for k in ('source', 'trigger', 'stop_hook_active', 'custom_instructions') if k in inp}

if ev == 'PostToolUse':
    cmd = (inp.get('tool_input') or {}).get('command', '')
    if 'PHASE1_DONE' in cmd: st['p1_done'] = True
    if 'PHASE2_DONE' in cmd: st['p2_done'] = True
    save(STATE, st)
    log(cmd=cmd, ctx=ctx(inp.get('transcript_path', '')))
elif ev == 'Stop':
    st['stops'] += 1
    c = ctx(inp.get('transcript_path', ''))
    if st['p1_done'] and not st['lowered']:
        s = load(LOCAL, {}); s['autoCompactWindow'] = 100000; save(LOCAL, s)
        st['lowered'] = True; save(STATE, st)
        time.sleep(3)  # give any settings watcher time to see the change
        log(action='lowered window to 100k, blocking stop', ctx=c, **keys)
        print(json.dumps({'decision': 'block', 'reason': 'smart-compact: Phase 1 is recorded complete. Begin Phase 2 now.'}))
    else:
        save(STATE, st)
        log(action='allow stop', ctx=c, state=st, **keys)
elif ev == 'SessionStart' and inp.get('source') == 'compact':
    s = load(LOCAL, {}); s.pop('autoCompactWindow', None); save(LOCAL, s)
    st['compacted'] = True; save(STATE, st)
    log(action='restored window, injecting brief', **keys)
    brief = ('SMART-COMPACT BRIEF (injected after compaction): Phase 1 is complete. You are now on Phase 2. '
             'Phase 2 steps: (a) run `echo PHASE2_START`; (b) run `echo PHASE2_DONE PINEAPPLE`; '
             '(c) end your turn with one line stating the codeword from this brief.')
    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': brief}}))
else:
    summ = inp.get('compact_summary') or inp.get('summary')
    log(input_keys=sorted(inp.keys()), summary_chars=len(summ) if isinstance(summ, str) else None, **keys)
