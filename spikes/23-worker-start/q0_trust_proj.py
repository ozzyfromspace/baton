"""L1: first start in the untrusted scratch repo `proj`, with an initial prompt: trust dialog, then is the prompt sent?"""
import os, sys, time, json
from drv import Drv, report, last_answer, HERE
proj = os.path.join(HERE, 'proj'); log = os.path.join(HERE, 'logs', 'q0.log')
d = Drv(proj, ['--permission-mode', 'acceptEdits', 'Reply with the code word from your session context and nothing else.'],
        log, name='q0', codeword='MARMOT-2290')
d.wait(lambda: d.trust_visible() or d.count('UserPromptSubmit') > 0, 30, 'trust dialog or prompt')
print('trust dialog visible:', d.trust_visible(), 'hooks so far:', [r['ev'] for r in d.recs()])
time.sleep(1.0); d.dump('trust-dialog')
t_trust = time.time(); d.accept_trust()
ok = d.wait_ev('Stop', 0, 90)
d.wait_quiet(2, 20)
print('UserPromptSubmit after trust accept (s):', round(d.first('UserPromptSubmit')['t'] - t_trust, 2) if d.first('UserPromptSubmit') else None)
print('keys sent (only the trust answer):', d.keys_sent)
print('answer:', repr(last_answer(d)))
d.dump('after-stop')
print('tree before /exit:', d.tree())
d.end_exit(); print('reap:', d.reap(15)); time.sleep(1)
report(d, 'q0'); d.save('run'); d.kill()
