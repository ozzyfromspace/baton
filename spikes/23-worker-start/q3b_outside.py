"""Q3b/Q4c: worker in a worktree OUTSIDE the trusted project (outside/P2). Trust dialog? Ended by closing the PTY master (SIGHUP)."""
import os, sys, time, json, uuid
from drv import Drv, report, last_answer, HERE, rel, short
cwd = os.path.join(HERE, 'outside', 'P2'); log = os.path.join(HERE, 'logs', 'q3b.log')
d = Drv(cwd, ['--session-id', str(uuid.uuid4()), '--permission-mode', 'acceptEdits', 'Reply with the code word from your session context and nothing else.'],
        log, name='q3b', codeword='HERON-3344')
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 60, 'trust dialog or Stop')
shown = d.trust_visible(); print('trust dialog shown:', shown, 'hooks before answering:', [r['ev'] for r in d.recs()])
if shown:
    time.sleep(1); d.dump('trust-dialog'); t_tr = time.time(); d.accept_trust()
d.wait_ev('Stop', 0, 90); d.wait_quiet(2, 20); d.dump('after-stop')
g = lambda ev: rel(d.first(ev)['t'], d.t0) if d.first(ev) else None
print('timings: SessionStart', g('SessionStart'), 'UserPromptSubmit', g('UserPromptSubmit'), 'Stop', g('Stop'))
print('answer:', repr(last_answer(d)), 'keys sent:', d.keys_sent)
print('tree before close:', d.tree())
t = time.time(); d.end_close_master(); print('reap:', d.reap(15), 'in', round(time.time() - t, 2), 's'); time.sleep(2)
print('tree after:', d.tree(), 'pgid members:', d.pgid_members())
report(d, 'q3b'); d.save('run'); d.kill()
