#!/usr/bin/env python3
"""Q1b/Q6: dispatch a second background session inside a linked worktree the host made (like .baton/worktrees/P3):
trust inherited? worktree isolation skipped? Then the resource cost with two live sessions, and `claude respawn`."""
import json, os, re, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
LOG = os.path.join(HERE, 'out', 'hooks-w2.jsonl'); WT = os.path.join(PROJ, '.baton', 'worktrees', 'P3')
PROMPT = 'Spike test. Use the Edit tool to append the line "edited in P3" to notes.txt, then reply with the single word "done" and end your turn.'
argv = [os.path.join(HERE, 'c.sh'), '--bg', '--model', 'haiku', '--name', 'spike26-w2', '--permission-mode', 'acceptEdits',
        '--settings', settings(LOG), PROMPT]
t0 = time.time(); d = Drv(WT, argv, name='dispatch2')
d.wait(lambda: not d.alive or d.has('trustthisfolder'), 30, 'dispatcher exit or trust')
note(f'dispatch2: exited={not d.alive} after {time.time()-t0:.1f}s; trust dialog={d.has("trustthisfolder")}')
out = d.plain_tail().decode('utf-8', 'replace'); print(out[-600:])
short = re.search(r'backgrounded\W+([0-9a-f]{8})', out).group(1)
if d.alive: d.close()
wait(lambda: count(LOG, 'Stop') > 0, 90, 'Stop w2')
note(f'w2 {short}: first Stop after {time.time()-t0:.1f}s')
for r in hooks(LOG):
    i = r['input']; print(r['hms'], r['ev'], i.get('source') or i.get('tool_name') or '', i.get('cwd'))
print(subprocess.run(['git', 'worktree', 'list'], cwd=PROJ, capture_output=True, text=True).stdout)
print(subprocess.run(['git', 'status', '--short'], cwd=WT, capture_output=True, text=True).stdout)
time.sleep(5)
tr = fmt_tree(daemon_pid()); dump('q6-tree-two-sessions.txt', tr); print(tr)
rows = tree(daemon_pid())
total = sum(r['rss'] for _, r in rows) // 1024
note(f'two live bg sessions: {len(rows)} processes under the daemon, total RSS {total} MB')
# respawn
pid_before = next(a['pid'] for a in agents() if isinstance(a, dict) and a.get('id') == short)
n_ss = count(LOG, 'SessionStart'); t1 = time.time()
rc, o, el = cc('respawn', short); note(f'respawn {short}: rc={rc} {el:.1f}s: {o.strip()}')
wait(lambda: count(LOG, 'SessionStart') > n_ss, 60, 'SessionStart after respawn')
time.sleep(3)
a = next(a for a in agents() if isinstance(a, dict) and a.get('id') == short)
note(f'after respawn: pid {pid_before} -> {a.get("pid")}, status {a.get("status")}/{a.get("state")}, '
     f'SessionStart {[ (r["input"].get("source"), r["input"].get("session_id")) for r in hooks(LOG) if r["ev"] == "SessionStart" and r["t"] >= t1]}, '
     f'SessionEnd {[ r["input"].get("reason") for r in hooks(LOG) if r["ev"] == "SessionEnd" and r["t"] >= t1]}')
