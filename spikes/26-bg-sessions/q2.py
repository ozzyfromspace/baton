#!/usr/bin/env python3
"""Q2: give an idle background session its next prompt by posting to its inbox socket from a host process
(not a child of the session). Watch hooks, agents status and `claude logs`."""
import json, os, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
LOG = os.path.join(HERE, 'out', sys.argv[1]); SHORT = sys.argv[2]; label = sys.argv[3]; text = sys.argv[4]; extra = sys.argv[5:]
sock = next(r['env']['CLAUDE_CODE_MESSAGING_SOCKET'] for r in reversed(hooks(LOG)) if r['env'].get('CLAUDE_CODE_MESSAGING_SOCKET'))
n_ups, n_stop = count(LOG, 'UserPromptSubmit'), count(LOG, 'Stop')
st = lambda: [(a.get('status'), a.get('state')) for a in agents() if isinstance(a, dict) and a.get('id') == SHORT]
note(f'{label}: status before {st()}; posting to {sock}')
t0 = time.time()
print(subprocess.run(['python3', os.path.join(HERE, 'post.py'), sock, text, *extra], capture_output=True, text=True).stdout)
seen = []
while time.time() - t0 < 90:
    s = st(); el = round(time.time() - t0, 1)
    if not seen or seen[-1][1] != s: seen.append((el, s))
    if count(LOG, 'Stop') > n_stop: break
    time.sleep(0.4)
note(f'{label}: status timeline {seen}')
note(f'{label}: UserPromptSubmit +{count(LOG, "UserPromptSubmit") - n_ups}, Stop +{count(LOG, "Stop") - n_stop} after {time.time()-t0:.1f}s')
time.sleep(2)
for r in hooks(LOG):
    if r['t'] >= t0 - 0.5:
        i = r['input']
        print(r['hms'], r['ev'], i.get('tool_name') or i.get('notification_type') or '', json.dumps(i.get('prompt'))[:900] if 'prompt' in i else '', (i.get('last_assistant_message') or '')[:200])
rc, out, _ = cc('logs', SHORT)
open(os.path.join(HERE, 'out', f'{label}-logs.txt'), 'w').write(out)
