#!/usr/bin/env python3
"""Q5: a background session asks a question (AskUserQuestion) or hits a permission prompt while nobody is attached.
What do `claude agents --json`, hooks and `claude logs` show, and how does a human (or a host) answer? usage: q5.py SHORT ask|perm"""
import json, os, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
from vt import Screen
LOG = os.path.join(HERE, 'out', 'hooks-w1.jsonl'); SHORT, kind = sys.argv[1], sys.argv[2]
via = sys.argv[3] if len(sys.argv) > 3 else 'message'   # message: post to the inbox socket; attach: type it through `claude attach`, then detach
me = lambda: next((a for a in agents() if isinstance(a, dict) and a.get('id') == SHORT), None)
sock = next(r['env']['CLAUDE_CODE_MESSAGING_SOCKET'] for r in reversed(hooks(LOG)) if r['env'].get('CLAUDE_CODE_MESSAGING_SOCKET'))
TEXT = {'ask': 'Use the AskUserQuestion tool (load it with ToolSearch if needed) to ask me one question: "Which color?" with the options Red, Green and Blue. '
               'After I answer, reply with the chosen color in one word and end your turn.',
        'perm': 'Run exactly this command with the Bash tool: python3 -c "print(6*7)"  and then reply with its output in one word and end your turn.'}[kind]
n_stop = count(LOG, 'Stop'); t0 = time.time()
note(f'Q5 {kind}: posting; status {me() and (me()["status"], me().get("state"))}')
if via == 'message':
    print(subprocess.run(['python3', os.path.join(HERE, 'post.py'), sock, TEXT, '--from', 'baton-host'], capture_output=True, text=True).stdout)
else:
    t = Drv(PROJ, [os.path.join(HERE, 'c.sh'), 'attach', SHORT], rows=40, cols=140, name='typist')
    t.wait(lambda: len(t.chunks) > 0, 15, 'first bytes'); t.wait_quiet(1.0, 10)
    t.type(TEXT, delay=0.005); t.enter()
    wait(lambda: count(LOG, 'UserPromptSubmit', lambda r: r['t'] >= t0) > 0, 20, 'UserPromptSubmit')
    t.key(b'\x1a'); t.wait(lambda: not t.alive, 10, 'typist detach')
    note(f'typed through attach and detached (alive={t.alive})')
    if t.alive: t.close()
blocker = (lambda r: r['ev'] == 'PreToolUse' and r['input'].get('tool_name') == 'AskUserQuestion') if kind == 'ask' else \
          (lambda r: r['ev'] == 'PermissionRequest')
wait(lambda: any(blocker(r) for r in hooks(LOG) if r['t'] >= t0), 60, 'question/permission hook')
tb = time.time(); note(f'{kind}: blocking hook after {tb-t0:.1f}s')
seen = []
while time.time() - tb < 75:      # long enough for the 60 s idle Notification
    a = me(); s = (a or {}).get('status'), (a or {}).get('state')
    if not seen or seen[-1][1] != s: seen.append((round(time.time() - tb, 1), s))
    time.sleep(1)
note(f'{kind}: status timeline while unanswered (s after blocking hook) {seen}')
dump(f'q5-{kind}-{via}-agent.json', me())
print(json.dumps(me(), indent=1))
rc, out, _ = cc('logs', SHORT); open(os.path.join(HERE, 'out', f'q5-{kind}-{via}-logs.txt'), 'w').write(out)
for r in hooks(LOG):
    if r['t'] >= t0:
        i = r['input']
        print(r['hms'], r['ev'], i.get('tool_name') or '', i.get('notification_type') or '', (i.get('message') or '')[:120], json.dumps(i.get('tool_input'))[:200] if r['ev'] in ('PreToolUse', 'PermissionRequest') else '')
# a human answers by attaching
v = Drv(PROJ, [os.path.join(HERE, 'c.sh'), 'attach', SHORT], rows=40, cols=140, name='human')
v.wait(lambda: len(v.chunks) > 0, 15, 'first bytes'); v.wait_quiet(1.5, 20)
s = Screen(40, 140); s.feed(v.bytes_between(0)); scr = '\n'.join(l.rstrip() for l in s.text().split('\n'))
open(os.path.join(HERE, 'out', f'q5-{kind}-{via}-attached.txt'), 'w').write(scr + '\n'); print(scr)
ta = time.time()
v.type('2' if kind == 'ask' else '1', delay=0.05)
if not wait(lambda: count(LOG, 'Stop') > n_stop, 15, 'Stop after digit'):
    v.key(b'\r'); wait(lambda: count(LOG, 'Stop') > n_stop, 30, 'Stop after Enter')
note(f'{kind}: answered via attach; Stop after {time.time()-ta:.1f}s; status {me() and (me()["status"], me().get("state"))}')
v.wait_quiet(1.5, 10)
for r in hooks(LOG):
    if r['t'] >= ta - 0.2:
        i = r['input']; print(r['hms'], r['ev'], i.get('tool_name') or '', json.dumps(i.get('tool_response'))[:300] if r['ev'].startswith('PostToolUse') else '', (i.get('last_assistant_message') or '')[:100])
v.key(b'\x1a'); v.wait(lambda: not v.alive, 10, 'detach'); v.save(os.path.join(HERE, 'out', f'q5-{kind}-{via}-viewer.json'))
if v.alive: v.close()
