#!/usr/bin/env python3
"""Q1: dispatch `claude --bg` (in a PTY, so a trust dialog could be answered) with baton-style hooks via --settings,
--session-id, --name, acceptEdits and narrow --allowedTools; then watch hooks, agents listing, processes, worktrees."""
import json, os, re, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
LOG = os.path.join(HERE, 'out', 'hooks-w1.jsonl')
SID = sys.argv[1] if len(sys.argv) > 1 else str(uuid.uuid4())
PROMPT = ('Spike test. Do exactly these steps: (1) use the Edit tool to append the line "edited by worker" to notes.txt; '
          '(2) run `pwd` with the Bash tool; (3) reply with the single word "done" and end your turn.')
argv = [os.path.join(HERE, 'c.sh'), '--bg', '--model', 'haiku', '--name', 'spike26-w1', '--session-id', SID,
        '--permission-mode', 'acceptEdits', '--allowedTools', 'Bash(pwd)', 'Bash(git status:*)', 'Bash(git log:*)',
        '--settings', settings(LOG), PROMPT]
note(f'Q1 dispatch: session-id {SID}')
t0 = time.time()
d = Drv(PROJ, argv, name='dispatch')
trusted = False
while d.alive and time.time() - t0 < 40:
    if d.has('trustthisfolder') and not trusted:
        d.mark('trust dialog shown in the dispatching terminal'); d.wait_quiet(0.8, 5)
        d.key(b'\x1b[B'); time.sleep(0.4); d.key(b'\r'); trusted = True
    time.sleep(0.2)
note(f'dispatcher exited={not d.alive} after {time.time()-t0:.1f}s, trust dialog={trusted}')
time.sleep(0.5)
raw = b''.join(c for _, c in d.chunks)
open(os.path.join(HERE, 'out', 'q1-dispatch-raw.txt'), 'wb').write(raw)
plain = d.plain_tail().decode('utf-8', 'replace')
open(os.path.join(HERE, 'out', 'q1-dispatch-plain.txt'), 'w').write(plain)
print('---- dispatcher output (plain) ----'); print(plain[-3000:]); print('----')
d.close()
note(f'daemon pid {daemon_pid()}')
wait(lambda: count(LOG, 'Stop') >= 1, 120, 'first Stop')
note(f'first Stop after {time.time()-t0:.1f}s')
time.sleep(3)
dump('q1-agents.json', agents())
dump('q1-tree.txt', fmt_tree(daemon_pid() or 0))
print(fmt_tree(daemon_pid() or 0))
print(json.dumps(mine(agents()), indent=1))
for r in hooks(LOG):
    i = r['input']
    print(r['hms'], r['ev'], i.get('session_id'), 'cwd=', i.get('cwd'), i.get('source') or i.get('tool_name') or i.get('notification_type') or '', (i.get('prompt') or '')[:80], r['env'].get('CLAUDE_CODE_MESSAGING_SOCKET'))
print(subprocess.run(['git', 'worktree', 'list'], cwd=PROJ, capture_output=True, text=True).stdout)
