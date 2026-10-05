"""Spike 15: when does Claude Code fire the idle_prompt notification (and is it a reliable sign that no turn
or dialog is open)? Logs Notification, Stop and UserPromptSubmit hooks over 150s idle, then 100s with a draft."""
import json, os, shutil, sys, time
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'common'))
from scenario import Driver
here = os.path.dirname(os.path.abspath(__file__))
work = os.path.join(here, 'work'); shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
log = os.path.join(work, 'notif.log')
hook = {'type': 'command', 'command': f'python3 -c "import sys,json,time; d=json.load(sys.stdin); open(\'{log}\',\'a\').write(json.dumps({{\'t\':time.time(),\'ev\':d.get(\'hook_event_name\'),\'type\':d.get(\'notification_type\'),\'msg\':d.get(\'message\')}})+\'\\n\')"'}
settings = {'hooks': {ev: [{'hooks': [hook]}] for ev in ['Notification', 'Stop', 'UserPromptSubmit']}}
d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps(settings), 'Reply with the single word ok.'])
d.trust()
t0 = time.time()
time.sleep(150)
# Then: type a draft and leave it (does a draft suppress idle_prompt?)
d.type('half-typed draft'); time.sleep(100)
d.key(b'\x15'); time.sleep(1)
d.type('/exit'); d.enter(); time.sleep(3); d.close()
for l in open(log):
    r = json.loads(l); print(round(r['t'] - t0, 1), r['ev'], r['type'], r['msg'])
