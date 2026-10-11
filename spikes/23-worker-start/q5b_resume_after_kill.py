"""Q5b: resume the worker that was ended mid-tool by closing its PTY (q4 hup), from its worktree. Does it know the command was cut off?"""
import os, sys, time, json
from drv import Drv, report, last_answer, HERE, rel, short
sid = json.loads(open(os.path.join(HERE, 'logs', 'q4-hup.log')).readline())['input']['session_id']
cwd = os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1'); log = os.path.join(HERE, 'logs', 'q5b.log')
p = 'Without using tools: did the command you ran earlier in this conversation finish, and what was its result? One line.'
d = Drv(cwd, ['--resume', sid, '--permission-mode', 'acceptEdits', '--', p], log, name='q5b')
d.wait(lambda: d.count('Stop') > 0 or not d.alive, 60, 'Stop'); d.wait_quiet(2, 20); d.dump('end')
print('answer:', repr(last_answer(d)), '| session ids:', sorted({(r.get('input') or {}).get('session_id') for r in d.recs()}), 'asked', sid)
d.end_exit(); print('reap:', d.reap(10)); time.sleep(1); print('tree after:', d.tree())
report(d, 'q5b'); d.save('run'); d.kill()
