"""Q5: resume the Q1 worker (session made in proj/.baton/worktrees/P1, ended by /exit).
Usage: q5_resume.py same|proj|elsewhere   (cwd = P1 | proj | elsewhere/, a non-git folder)
       q5_resume.py reuse                 (`--session-id <same id>` in P1 instead of --resume)"""
import os, sys, time, json
from drv import Drv, report, last_answer, HERE, rel, short
v = sys.argv[1]
sid = open(os.path.join(HERE, 'out', 'q1-session-id.txt')).read().strip()
cwd = {'same': os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1'), 'proj': os.path.join(HERE, 'proj'),
       'elsewhere': os.path.join(HERE, 'elsewhere'), 'reuse': os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1')}[v]
log = os.path.join(HERE, 'logs', f'q5-{v}.log')
prompt = 'Without using tools: what code word and what number were you given earlier in this conversation? Reply on one line.'
flag = ['--session-id', sid] if v == 'reuse' else ['--resume', sid]
d = Drv(cwd, flag + ['--permission-mode', 'acceptEdits', '--', prompt], log, name=f'q5-{v}')   # no CODEWORD this time
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0 or not d.alive, 60, 'trust dialog, Stop or exit')
if d.trust_visible() and d.count('Stop') == 0:
    print('trust dialog shown'); time.sleep(1); d.dump('trust-dialog'); d.accept_trust()
    d.wait(lambda: d.count('Stop') > 0 or not d.alive, 60, 'Stop or exit')
time.sleep(2); txt = d.dump('end')
print('alive:', d.alive, '| hooks:', [r['ev'] for r in d.recs()])
if d.count('Stop'):
    print('answer:', repr(last_answer(d)))
    print('session ids in hooks:', sorted({(r.get('input') or {}).get('session_id') for r in d.recs()}), '(asked for', sid + ')')
else:
    print('screen (non-blank lines):\n' + short('\n'.join(l for l in txt.split('\n') if l.strip())[-1500:]))
if d.alive: d.end_exit()
print('reap:', d.reap(10)); time.sleep(1); print('tree after:', d.tree())
report(d, f'q5-{v}'); d.save('run'); d.kill()
