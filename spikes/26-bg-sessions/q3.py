#!/usr/bin/env python3
"""Q3: stop a background session, resume it into a PTY we own (`claude --resume <id>`), type into it, exit;
then continue it in the background again (`claude --resume <id> --bg "<prompt>"`)."""
import json, os, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from common import *
from drv import Drv
LOG1 = os.path.join(HERE, 'out', 'hooks-w1.jsonl'); LOG2 = os.path.join(HERE, 'out', 'hooks-resume-pty.jsonl'); LOG3 = os.path.join(HERE, 'out', 'hooks-resume-bg.jsonl')
SHORT = sys.argv[1]; SID = hooks(LOG1)[0]['input']['session_id']
me = lambda: next((a for a in agents() if isinstance(a, dict) and a.get('id') == SHORT), None)
pid0 = me()['pid']
t0 = time.time(); rc, out, el = cc('stop', SHORT)
note(f'claude stop: rc={rc} in {el:.1f}s: {out.strip()}')
time.sleep(2)
alive = subprocess.run(['ps', '-p', str(pid0)], capture_output=True).returncode == 0
note(f'after stop: session pid {pid0} alive={alive}; agents entry {me()}; SessionEnd hooks {count(LOG1, "SessionEnd")}')
dump('q3-tree-after-stop.txt', fmt_tree(daemon_pid())); print(fmt_tree(daemon_pid()))
# resume into a PTY we own
argv = [os.path.join(HERE, 'c.sh'), '--resume', SID, '--model', 'haiku', '--permission-mode', 'acceptEdits', '--settings', settings(LOG2)]
t1 = time.time(); d = Drv(PROJ, argv, rows=40, cols=140, name='own')
wait(lambda: count(LOG2, 'SessionStart') > 0 or not d.alive, 40, 'SessionStart in owned PTY')
d.wait_quiet(2, 30)
note(f'owned PTY: alive={d.alive} after {time.time()-t1:.1f}s; SessionStart {[(r["input"].get("source"), r["input"].get("session_id"), r["input"].get("cwd")) for r in hooks(LOG2) if r["ev"] == "SessionStart"]}')
open(os.path.join(HERE, 'out', 'q3-own-start.txt'), 'w').write(d.plain_tail(4000).decode('utf-8', 'replace'))
n = count(LOG2, 'Stop')
d.type('Resumed in a PTY the host owns: reply with the single word RESUMED-PTY and end your turn. No tools.', delay=0.01); d.enter()
wait(lambda: count(LOG2, 'Stop') > n, 60, 'Stop in owned PTY')
d.wait_quiet(2, 20)
own = [r for r in ps() if r['pid'] == d.pid]
note(f'owned PTY turn done; RSS of the interactive claude: {[(r["pid"], r["rss"] // 1024) for r in own]} MB; kids {[(r["pid"], r["rss"] // 1024, r["cmd"][:40]) for r in ps() if r["ppid"] == d.pid]}')
note(f'agents entry while resumed in our PTY: {me()}; listing for that session id: {[a for a in agents() if isinstance(a, dict) and a.get("sessionId") == SID]}')
for r in hooks(LOG2): print(r['hms'], r['ev'], r['input'].get('source') or '', r['input'].get('session_id'), (r['input'].get('last_assistant_message') or '')[:60], r['env'].get('CLAUDE_CODE_MESSAGING_SOCKET'))
d.type('/exit', delay=0.03); d.enter(); d.wait(lambda: not d.alive, 15, 'exit'); d.save(os.path.join(HERE, 'out', 'q3-own.json'))
if d.alive: d.close()
note(f'owned PTY exited; agents entry {me()}')
# continue it in the background again
t2 = time.time()
rc, out, el = cc('--resume', SID, '--bg', '--model', 'haiku', '--permission-mode', 'acceptEdits', '--settings', settings(LOG3),
                 'Continued with --resume --bg: reply with the single word RESUMED-BG and end your turn. No tools.', timeout=60)
note(f'--resume --bg: rc={rc} in {el:.1f}s: {out.strip()}')
wait(lambda: count(LOG3, 'Stop') > 0, 90, 'Stop of resumed bg session')
note(f'resumed bg: {[(r["ev"], r["input"].get("source"), r["input"].get("session_id")) for r in hooks(LOG3) if r["ev"] in ("SessionStart", "Stop")]} after {time.time()-t2:.1f}s')
print(json.dumps([a for a in agents() if isinstance(a, dict) and HERE in (a.get('cwd') or '')], indent=1))
print(fmt_tree(daemon_pid()))
