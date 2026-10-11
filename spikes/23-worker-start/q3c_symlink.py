"""Q3c: worker started with cwd = proj/.baton/w/PN, a symlink to outside/PN. Usage: q3c_symlink.py P3 (PWD unset) | P4 pwd (PWD=symlink)."""
import os, sys, time, json, uuid
from drv import Drv, report, last_answer, HERE, rel, short
p = sys.argv[1]; with_pwd = len(sys.argv) > 2
link = os.path.join(HERE, 'proj', '.baton', 'w', p); log = os.path.join(HERE, 'logs', f'q3c-{p}.log')
prompt = ('Use the Bash tool to run exactly this one command: pwd; pwd -P; echo "PWD=$PWD"; git rev-parse --show-toplevel; git branch --show-current'
          ' -- then reply with its output verbatim and nothing else.')
d = Drv(link, ['--session-id', str(uuid.uuid4()), '--permission-mode', 'acceptEdits', '--allowedTools', 'Bash', '--', prompt],
        log, name=f'q3c-{p}', pwd=link if with_pwd else None)
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 90, 'trust dialog or Stop')
shown = d.trust_visible(); print('PWD passed:', with_pwd, '| trust dialog shown:', shown)
if shown:
    time.sleep(1); d.dump('trust-dialog'); print('\n'.join(l for l in d.screen().split('\n') if 'private' in l))
    d.accept_trust(); d.wait_ev('Stop', 0, 90)
d.wait_quiet(2, 20); d.dump('after-stop')
print('answer:', repr(short(last_answer(d))))
for r in d.recs():
    if r['ev'] == 'PostToolUse': print('Bash tool_response:', short(json.dumps(r['input'].get('tool_response'))))
t = time.time(); d.end_exit(); print('reap:', d.reap(15)); time.sleep(1.5)
print('tree after:', d.tree())
report(d, f'q3c-{p}'); d.save('run'); d.kill()
