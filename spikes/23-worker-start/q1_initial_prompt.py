"""Q1/Q2/Q3a/Q4a: worker in proj/.baton/worktrees/P1 started with --session-id and an initial prompt; ended with /exit."""
import os, sys, time, json, uuid
from drv import Drv, report, last_answer, HERE, rel, short
cwd = os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1'); log = os.path.join(HERE, 'logs', 'q1.log')
sid = str(uuid.uuid4()); open(os.path.join(HERE, 'out', 'q1-session-id.txt'), 'w').write(sid)
prompt = 'Reply with the code word from your session context, then on a new line the word READY. Also remember the number 4817 for later.'
d = Drv(cwd, ['--session-id', sid, '--permission-mode', 'acceptEdits', prompt], log, name='q1', codeword='PELICAN-7731')
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 90, 'trust dialog or Stop')
print('trust dialog shown:', d.trust_visible())
d.wait_quiet(2, 20); d.dump('after-stop')
ss, ups, st = d.first('SessionStart'), d.first('UserPromptSubmit'), d.first('Stop')
print('timings (s after launch): SessionStart', rel(ss['t'], d.t0), 'UserPromptSubmit', rel(ups['t'], d.t0), 'Stop', rel(st['t'], d.t0))
print('keys sent before first Stop:', d.keys_sent)
print('answer:', repr(last_answer(d)))
print('session_id requested', sid, '| in hooks', sorted({(r.get('input') or {}).get('session_id') for r in d.recs()}))
tp = ss['input']['transcript_path']; print('transcript', short(tp), 'exists', os.path.exists(tp))
print('tree before /exit:', d.tree())
t = time.time(); d.end_exit(); print('reap:', d.reap(15), 'in', round(time.time() - t, 2), 's'); time.sleep(1.5)
print('tree after exit:', d.tree(), 'pgid members:', d.pgid_members())
report(d, 'q1'); d.save('run'); d.kill()
