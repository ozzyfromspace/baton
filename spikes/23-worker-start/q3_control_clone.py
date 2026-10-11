"""Control for Q3b: a CLONE of proj (not a worktree) outside the project. Expect the trust dialog; answer the default "No, exit"."""
import os, sys, time, json, uuid
from drv import Drv, report, HERE
cwd = os.path.join(HERE, 'outside', 'clone'); log = os.path.join(HERE, 'logs', 'q3ctl.log')
d = Drv(cwd, ['--permission-mode', 'acceptEdits', 'Reply OK.'], log, name='q3ctl')
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 60, 'trust dialog or Stop')
print('trust dialog shown:', d.trust_visible(), 'hooks:', [r['ev'] for r in d.recs()])
time.sleep(1); txt = d.dump('trust-dialog')
print('\n'.join(l for l in txt.split('\n') if l.strip())[-900:])
d.key(b'\r'); d.mark('Enter on the default (No, exit)')
print('reap:', d.reap(15)); print('tree after:', d.tree())
report(d, 'q3ctl'); d.save('run'); d.kill()
