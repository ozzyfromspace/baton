"""Q3d: `claude -w spikeP1` from proj: where is the worktree, which branch, trust prompt? What happens on /exit?"""
import os, sys, time, json, uuid, subprocess
from drv import Drv, report, last_answer, HERE, rel, short, git
proj = os.path.join(HERE, 'proj'); log = os.path.join(HERE, 'logs', 'q3d.log')
before = git('worktree', 'list', cwd=proj)
prompt = 'Use the Bash tool to run exactly: pwd; git branch --show-current; git log --oneline -1 -- then reply with its output verbatim and nothing else.'
d = Drv(proj, ['-w', 'spikeP1', '--session-id', str(uuid.uuid4()), '--permission-mode', 'acceptEdits', '--allowedTools', 'Bash', '--', prompt],
        log, name='q3d')
d.wait(lambda: d.trust_visible() or d.count('Stop') > 0, 90, 'trust dialog or Stop')
print('trust dialog shown:', d.trust_visible())
d.wait_quiet(2, 20); d.dump('after-stop')
print('answer:', repr(short(last_answer(d))))
print('worktree list after start:\n' + short(git('worktree', 'list', cwd=proj)))
print('branches:', git('branch', '-a', '--format=%(refname:short) %(upstream:short)', cwd=proj).split('\n'))
print('proj status:', repr(git('status', '--short', '--ignored', cwd=proj)))
t = time.time(); d.end_exit()
d.wait(lambda: not d.alive, 8, 'exit after /exit')
time.sleep(1); txt = d.dump('after-exit')
print('screen after /exit (last lines):\n' + '\n'.join(l for l in txt.split('\n') if l.strip())[-1500:])
if d.alive:
    print('still alive after /exit -> a dialog is up')
print('reap:', d.reap(5))
print('worktree list after exit:\n' + short(git('worktree', 'list', cwd=proj)))
report(d, 'q3d'); d.save('run')
