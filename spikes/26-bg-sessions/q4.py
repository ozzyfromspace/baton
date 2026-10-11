#!/usr/bin/env python3
"""Q3/Q4: `claude attach` in PTYs we drive. What a fresh viewer sees, typing a prompt and /compact through it,
a second simultaneous viewer, detaching (Ctrl+Z, /exit), and whether the session survives."""
import json, os, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
from vt import Screen
LOG = os.path.join(HERE, 'out', 'hooks-w1.jsonl'); SHORT = sys.argv[1]
A = lambda: [(a.get('status'), a.get('state'), a.get('pid')) for a in agents() if isinstance(a, dict) and a.get('id') == SHORT]
def render(d, rows, cols, label, since=None):
    s = Screen(rows, cols); s.feed(d.bytes_between(since or 0))
    txt = '\n'.join(l.rstrip() for l in s.text().split('\n'))
    open(os.path.join(HERE, 'out', f'q4-{label}.txt'), 'w').write(txt + '\n')
    return txt

argv = [os.path.join(HERE, 'c.sh'), 'attach', SHORT]
note(f'Q4: status before attach {A()}')
t0 = time.time()
v1 = Drv(PROJ, argv, rows=40, cols=140, name='v1')
v1.wait(lambda: len(v1.chunks) > 0, 15, 'first bytes'); t_first = time.time() - t0
v1.wait_quiet(1.5, 20)
note(f'v1 first byte after {t_first:.2f}s; quiet after {time.time()-t0:.1f}s; bytes {sum(len(c) for _, c in v1.chunks)}')
print(render(v1, 40, 140, 'v1-fresh'))
note(f'status while attached {A()}')
dump('q4-tree-attached.txt', fmt_tree(daemon_pid())); print(fmt_tree(daemon_pid()))
for r in ps():
    if 'attach' in r['cmd'] and SHORT in r['cmd']: print('ATTACH PROC', r['pid'], r['ppid'], r['rss'] // 1024, 'MB', r['cmd'][:120])

# type a prompt through the viewer
n = count(LOG, 'Stop'); u = count(LOG, 'UserPromptSubmit')
v1.type('Typed through claude attach: reply with the single word ATTACH-OK and end your turn. No tools.', delay=0.01); v1.enter()
wait(lambda: count(LOG, 'Stop') > n, 60, 'Stop after typed prompt')
note(f'typed prompt: UserPromptSubmit +{count(LOG, "UserPromptSubmit")-u}, Stop +{count(LOG, "Stop")-n}')
v1.wait_quiet(1.5, 20)

# second viewer at another size while v1 is attached
t2 = time.time()
v2 = Drv(PROJ, argv, rows=30, cols=100, name='v2')
v2.wait(lambda: len(v2.chunks) > 0, 15, 'v2 first bytes'); v2.wait_quiet(1.5, 20); v1.wait_quiet(1.0, 10)
note(f'v2 attached: alive v1={v1.alive} v2={v2.alive}; v1 bytes since v2 start {len(v1.bytes_between(t2))}')
print(render(v2, 30, 100, 'v2-fresh'))
print('---- v1 after v2 attached (bytes since v2 start rendered on v1 full stream) ----')
print(render(v1, 40, 140, 'v1-after-v2'))
note(f'status with two viewers {A()}')

# /compact typed through v2
n = count(LOG, 'PostCompact'); t3 = time.time()
v2.type('/compact', delay=0.03); v2.enter()
wait(lambda: count(LOG, 'PostCompact') > n, 180, 'PostCompact')
note(f'/compact through attach: PreCompact={count(LOG, "PreCompact")} PostCompact={count(LOG, "PostCompact")} in {time.time()-t3:.1f}s; '
     f'SessionStart(compact)={count(LOG, "SessionStart", lambda r: r["input"].get("source") == "compact")}')
v2.wait_quiet(2, 30); v1.wait_quiet(1, 10)
note(f'v1 bytes during compact (mirror?) {len(v1.bytes_between(t3))}, v2 {len(v2.bytes_between(t3))}')
print(render(v2, 30, 100, 'v2-after-compact'))

# detach v1 with Ctrl+Z
v1.key(b'\x1a'); v1.wait(lambda: not v1.alive, 10, 'v1 exit after Ctrl+Z')
note(f'v1 after Ctrl+Z: alive={v1.alive}; session {A()}')
open(os.path.join(HERE, 'out', 'q4-v1-tail.txt'), 'w').write(v1.plain_tail(3000).decode('utf-8', 'replace'))
# detach v2 with /exit
v2.type('/exit', delay=0.03); v2.enter(); v2.wait(lambda: not v2.alive, 15, 'v2 exit after /exit')
note(f'v2 after /exit: alive={v2.alive}; session {A()}')
open(os.path.join(HERE, 'out', 'q4-v2-tail.txt'), 'w').write(v2.plain_tail(3000).decode('utf-8', 'replace'))
for d, nm in ((v1, 'v1'), (v2, 'v2')):
    d.save(os.path.join(HERE, 'out', f'q4-{nm}.json'))
    if d.alive: d.close()
time.sleep(2)
note(f'after both viewers gone: {A()}')
for r in hooks(LOG):
    if r['t'] >= t0:
        i = r['input']; print(r['hms'], r['ev'], i.get('source') or i.get('tool_name') or i.get('trigger') or '', json.dumps(i.get('prompt'))[:120] if 'prompt' in i else '', (i.get('last_assistant_message') or '')[:80])
