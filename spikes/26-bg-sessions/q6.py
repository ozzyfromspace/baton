#!/usr/bin/env python3
"""Q6: resource cost. Sample the daemon's process tree (live bg sessions, idle) and a plain interactive claude in a PTY
we own, each after one short turn."""
import json, os, statistics, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
LOG = os.path.join(HERE, 'out', 'hooks-interactive.jsonl')
d = Drv(PROJ, [os.path.join(HERE, 'c.sh'), '--model', 'haiku', '--permission-mode', 'acceptEdits', '--settings', settings(LOG)], rows=50, cols=160, name='plain')
wait(lambda: count(LOG, 'SessionStart') > 0, 30, 'SessionStart'); d.wait_quiet(2, 20)
d.type('Reply with the single word OK and end your turn. No tools.', delay=0.01); d.enter()
wait(lambda: count(LOG, 'Stop') > 0, 60, 'Stop'); time.sleep(5)
def classify(r):
    c = r['cmd']
    if 'daemon run' in c: return 'daemon'
    if 'bg-pty-host' in c: return 'pty-host'
    if 'bg-spare' in c: return 'session-or-spare'
    return c.split()[0][-20:]
samples = {}
for _ in range(15):
    for dd, r in tree(daemon_pid()) + tree(d.pid):
        samples.setdefault(r['pid'], []).append((r['rss'] / 1024, float(r['cpu']), classify(r), r['ppid'], r['cmd'][:60]))
    time.sleep(1)
live = {a['pid'] for a in agents() if isinstance(a, dict) and a.get('kind') == 'background' and a.get('pid')}
lines = []
for pid, s in samples.items():
    kind = s[0][2]
    if kind == 'session-or-spare': kind = 'bg session' if pid in live else 'spare (pre-warmed, unclaimed)'
    if pid == d.pid: kind = 'plain interactive claude'
    lines.append(f'{pid:>6} {kind:32} rss {statistics.mean(x[0] for x in s):6.0f} MB  cpu {statistics.mean(x[1] for x in s):4.1f} %  ({s[0][4]})')
print('\n'.join(sorted(lines, key=lambda l: l.split()[1])))
dump('q6-resources.txt', '\n'.join(lines))
d.type('/exit', delay=0.03); d.enter(); d.wait(lambda: not d.alive, 15, 'exit')
if d.alive: d.close()
note(f'plain interactive exited: alive={d.alive}')
