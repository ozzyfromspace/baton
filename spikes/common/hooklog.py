#!/usr/bin/env python3
"""Generic spike hook: log the full input of any event, plus a few opt-in behaviors.

usage: hooklog.py <Event> [--tag T] [--log PATH] [--mark MARKER] [--request-compact]
                  [--brief TEXT] [--rewake TEXT]

  --mark MARKER       PostToolUse: if a Bash command contains MARKER, set state flag `marked`
  --request-compact   Stop: once `marked`, write compact.request (the driver types /compact)
  --brief TEXT        SessionStart(compact): emit TEXT as additionalContext
  --rewake TEXT       exit 2 with TEXT on stderr (use with "asyncRewake": true)
"""
import json, os, sys, time

args = sys.argv[1:]
ev = args.pop(0)
opt = {}
while args:
    k = args.pop(0)
    opt[k] = args.pop(0) if k in ('--tag', '--log', '--mark', '--brief', '--rewake') else True

d = os.path.dirname(os.path.abspath(opt.get('--log') or os.path.join(os.getcwd(), 'hooks.log')))
log_path = opt.get('--log') or os.path.join(os.getcwd(), 'hooks.log')
state_path = os.path.join(d, 'hookstate.json')
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}

def shrink(v):
    if isinstance(v, str) and len(v) > 300:
        return v[:300] + f'…(+{len(v) - 300})'
    if isinstance(v, dict):
        return {k: shrink(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x) for x in v[:10]]
    return v

def state():
    try:
        return json.load(open(state_path))
    except Exception:
        return {}

def log(**kw):
    rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'tag': opt.get('--tag'),
           'env': {k: os.environ.get(k) for k in ('CLAUDE_PID', 'CLAUDE_CODE_SESSION_ID') if os.environ.get(k)},
           'input': shrink(inp), **kw}
    with open(log_path, 'a') as f:
        f.write(json.dumps(rec) + '\n')

out = None
st = state()
if ev == 'PostToolUse' and opt.get('--mark'):
    if opt['--mark'] in json.dumps(inp.get('tool_input', {})):
        st['marked'] = True; json.dump(st, open(state_path, 'w'))
if ev == 'Stop' and opt.get('--request-compact') and st.get('marked') and not st.get('requested'):
    st['requested'] = True; json.dump(st, open(state_path, 'w'))
    open(os.path.join(d, 'compact.request'), 'w').write('1')
if ev == 'SessionStart' and opt.get('--brief') and inp.get('source') == 'compact':
    out = {'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': opt['--brief']}}
log(emitted=bool(out), rewake=bool(opt.get('--rewake')))
if out:
    print(json.dumps(out))
if opt.get('--rewake'):
    sys.stderr.write(opt['--rewake'] + '\n')
    sys.exit(2)
