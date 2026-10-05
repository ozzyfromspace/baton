"""How does the AskUserQuestion dialog take keys? Variant 'down': Down arrow then Enter. Variant 'digit': "2"."""
import json, os, shutil, sys, time
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'common'))
from scenario import Driver
variant = sys.argv[1]
here = os.path.dirname(os.path.abspath(__file__))
work = os.path.join(here, 'work-' + variant); shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
log = os.path.join(work, 'hooks.log')
hook = {'type': 'command', 'command': f'python3 -c "import sys,json,time; d=json.load(sys.stdin); open(\'{log}\',\'a\').write(json.dumps({{\'t\':time.time(),\'ev\':d.get(\'hook_event_name\'),\'tool\':d.get(\'tool_name\'),\'resp\':d.get(\'tool_response\')}})+\'\\n\')"'}
settings = {'hooks': {ev: [{'hooks': [hook]}] for ev in ['PermissionRequest', 'PostToolUse', 'PostToolUseFailure', 'PermissionDenied', 'Stop']}}
prompt = ('Call the AskUserQuestion tool exactly once, with one question "baton: probe question?" (header "Probe") and exactly two options: '
          '"Checkpoint now" (description "first") and "Keep going" (description "second"). After the answer, reply with just the chosen label.')
d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps(settings), prompt])
d.trust()
def events():
    try: return [json.loads(l) for l in open(log)]
    except Exception: return []
d.wait(lambda: any(e['ev'] == 'PermissionRequest' for e in events()), 90, 'dialog open')
time.sleep(2); d.wait_quiet(1.0, 10)
if variant == 'down':
    d.key(b'\x1b[B'); time.sleep(0.6); d.key(b'\r')
else:
    d.key(b'2')
d.wait(lambda: any(e['ev'] in ('PostToolUse', 'PostToolUseFailure', 'PermissionDenied') for e in events()), 20, 'answer')
time.sleep(1)
# A review/submit step, if any, would still be waiting: note it, then press Enter once more.
if not any(e['ev'] in ('PostToolUse', 'PostToolUseFailure') for e in events()):
    print(variant, 'no answer after the keys; pressing Enter again'); d.key(b'\r')
    d.wait(lambda: any(e['ev'] in ('PostToolUse', 'PostToolUseFailure') for e in events()), 15, 'answer after second Enter')
d.wait(lambda: any(e['ev'] == 'Stop' for e in events()), 60, 'stop'); time.sleep(1)
d.type('/exit'); d.enter(); time.sleep(3); d.close()
for e in events():
    if e['ev'] != 'PermissionRequest':
        print(variant, e['ev'], e['tool'], json.dumps((e.get('resp') or {}).get('answers') if isinstance(e.get('resp'), dict) else e.get('resp'))[:200])
