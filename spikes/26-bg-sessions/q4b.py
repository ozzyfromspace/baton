#!/usr/bin/env python3
"""Q4b: which size wins with two viewers, and what happens to the first viewer's size when the second detaches;
then `claude logs` after all viewers left (does the session PTY go back to 200x50?)."""
import json, os, re, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
SHORT = sys.argv[1]
argv = [os.path.join(HERE, 'c.sh'), 'attach', SHORT]
rows_used = lambda b: sorted({int(x) for x in re.findall(rb'\x1b\[(\d+);1H', b)})
v1 = Drv(PROJ, argv, rows=40, cols=140, name='v1'); v1.wait(lambda: len(v1.chunks) > 0, 15, 'v1'); v1.wait_quiet(1.5, 15)
note(f'v1 alone (40x140): rows addressed {rows_used(v1.bytes_between(0))}')
t = time.time(); v2 = Drv(PROJ, argv, rows=30, cols=100, name='v2'); v2.wait(lambda: len(v2.chunks) > 0, 15, 'v2'); v2.wait_quiet(1.5, 15); v1.wait_quiet(1, 5)
note(f'v2 attached (30x100): v1 got {len(v1.bytes_between(t))} bytes, rows {rows_used(v1.bytes_between(t))}; v2 rows {rows_used(v2.bytes_between(0))}')
t = time.time(); v2.key(b'\x1a'); v2.wait(lambda: not v2.alive, 10, 'v2 detach'); time.sleep(0.5); v1.wait_quiet(1.5, 10)
note(f'v2 detached (alive={v2.alive}): v1 got {len(v1.bytes_between(t))} bytes after, rows {rows_used(v1.bytes_between(t))}')
t = time.time(); v1.setsize(35, 120); v1.wait_quiet(1.5, 10)
note(f'v1 resized its own terminal to 35x120: got {len(v1.bytes_between(t))} bytes, rows {rows_used(v1.bytes_between(t))}')
t = time.time(); v1.key(b'\x1b[D'); v1.wait_quiet(1.5, 10)
open(os.path.join(HERE, 'out', 'q4b-v1-left-arrow.txt'), 'w').write(v1.plain_tail(3000).decode('utf-8', 'replace'))
note(f'left arrow on empty prompt: v1 alive={v1.alive}; agent view shown={v1.has("describe a task") or v1.has("awaiting input")}')
v1.key(b'\x1a'); v1.wait(lambda: not v1.alive, 10, 'v1 exit'); note(f'Ctrl+Z from agent view: v1 alive={v1.alive}')
if v1.alive: v1.close()
time.sleep(1)
rc, out, _ = cc('logs', SHORT); b = out.encode()
note(f'claude logs after all viewers left: rows addressed {rows_used(b)[-5:]}, status {[ (a.get("status"), a.get("state")) for a in agents() if isinstance(a, dict) and a.get("id") == SHORT]}')
