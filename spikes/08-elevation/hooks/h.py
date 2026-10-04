#!/usr/bin/env python3
import json, os, sys, time
ev = sys.argv[1]
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
E = os.path.join(ROOT, '.elev')
sid = inp.get('session_id', '')

def log(**kw):
    with open(os.path.join(ROOT, 'hooks.log'), 'a') as f:
        f.write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, 'baton': bool(os.environ.get('BATON')), 'sid': sid[:8], **kw}) + '\n')

if ev == 'PostToolUse':
    cmd = (inp.get('tool_input') or {}).get('command', '')
    if 'AFTER_ELEVATION' in cmd:
        json.dump({'p2_done': True}, open(os.path.join(ROOT, '.sc', 'state.json'), 'w'))
    log(cmd=cmd[:120])
elif ev == 'Stop':
    if os.path.exists(os.path.join(E, f'kill-{sid}')):
        open(os.path.join(E, f'turn-ended-{sid}'), 'w').write('1')
        log(action='turn ended; elevation will restart claude')
        print(json.dumps({'systemMessage': 'baton: restarting this session under baton…'}))
    else:
        log()
elif ev == 'SessionStart':
    log(source=inp.get('source'))
    if os.environ.get('BATON') and inp.get('source') == 'resume':
        print(json.dumps({'systemMessage': 'baton: this session is now hosted by baton (elevated).'}))
elif ev == 'SessionStartRewake':
    pend = os.environ.get('BATON_PENDING_FILE')
    if os.environ.get('BATON') and inp.get('source') == 'resume' and pend and os.path.exists(pend):
        time.sleep(2)
        log(action='rewaking with pending action')
        sys.stderr.write(open(pend).read().strip() + '\n')
        sys.exit(2)
