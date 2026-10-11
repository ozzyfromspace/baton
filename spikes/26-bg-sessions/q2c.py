#!/usr/bin/env python3
"""Q2c: a message posted while the background session is busy (mid-turn). Delivered between tool calls, or as a new turn?
Also: priority "now"."""
import json, os, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
LOG = os.path.join(HERE, 'out', 'hooks-w1.jsonl'); SHORT = sys.argv[1]; prio = sys.argv[2] if len(sys.argv) > 2 else None
sock = next(r['env']['CLAUDE_CODE_MESSAGING_SOCKET'] for r in reversed(hooks(LOG)) if r['env'].get('CLAUDE_CODE_MESSAGING_SOCKET'))
post = lambda text, *x: print(subprocess.run(['python3', os.path.join(HERE, 'post.py'), sock, text, '--from', 'baton-host', *x], capture_output=True, text=True).stdout)
t0 = time.time(); n_stop = count(LOG, 'Stop')
post('Run `pwd` with the Bash tool four times, one call at a time (four separate tool calls, not one command). Then reply with "four done" and end your turn.')
wait(lambda: count(LOG, 'PostToolUse', lambda r: r['t'] >= t0 and r['input'].get('tool_name') == 'Bash') >= 1, 60, 'first pwd')
note(f'busy-test: first pwd done after {time.time()-t0:.1f}s; posting second message (priority={prio})')
tm = time.time()
post('Second note from the host: when you finish, also include the word KIWI in your final reply.', *(['--priority', prio] if prio else []))
wait(lambda: count(LOG, 'Stop') > n_stop, 90, 'Stop')
time.sleep(4); wait(lambda: False, 6, 'settle (any extra turn)')
for r in hooks(LOG):
    if r['t'] >= t0:
        i = r['input']; print(f"{r['t']-tm:+6.1f}s", r['ev'], i.get('tool_name') or '', json.dumps(i.get('prompt'))[:100] if 'prompt' in i else '', (i.get('last_assistant_message') or '')[:120])
