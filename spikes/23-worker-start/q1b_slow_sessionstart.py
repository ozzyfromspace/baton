"""Q1b/Q4b: does the initial prompt wait for a slow (4 s) SessionStart hook, and does its context still arrive? Ended with SIGTERM."""
import os, sys, time, json, uuid
from drv import Drv, report, last_answer, HERE, rel, short
cwd = os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1'); log = os.path.join(HERE, 'logs', 'q1b.log')
sid = str(uuid.uuid4())
d = Drv(cwd, ['--session-id', sid, '--permission-mode', 'acceptEdits', 'Reply with the code word from your session context and nothing else.'],
        log, name='q1b', codeword='OSPREY-5150', hook_sleep=4)
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 90, 'trust dialog or Stop')
print('trust dialog shown:', d.trust_visible())
d.wait_quiet(2, 20); d.dump('after-stop')
g = lambda ev: rel(d.first(ev)['t'], d.t0) if d.first(ev) else None
print('timings: SessionStart', g('SessionStart'), 'SessionStart-done', g('SessionStart-done'), 'UserPromptSubmit', g('UserPromptSubmit'), 'Stop', g('Stop'))
print('answer:', repr(last_answer(d)), 'keys sent:', d.keys_sent)
print('tree before SIGTERM:', d.tree())
t = time.time(); d.end_sigterm(); print('reap:', d.reap(15), 'in', round(time.time() - t, 2), 's'); time.sleep(1.5)
print('tree after:', d.tree(), 'pgid members:', d.pgid_members())
report(d, 'q1b'); d.save('run'); d.kill()
