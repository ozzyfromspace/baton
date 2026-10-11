"""Q4 (mid-turn): end a worker while its Bash tool runs a 40 s command. Usage: q4_midturn.py sigterm|hup|exit|sigkill"""
import os, sys, time, json, uuid, subprocess
from drv import Drv, report, last_answer, HERE, rel, short
v = sys.argv[1]
cwd = os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P1'); log = os.path.join(HERE, 'logs', f'q4-{v}.log')
p = "Use the Bash tool (not in the background) to run exactly: python3 -c 'import time; time.sleep(40)'; echo SLEPT -- then reply DONE."
d = Drv(cwd, ['--session-id', str(uuid.uuid4()), '--permission-mode', 'acceptEdits', '--allowedTools', 'Bash', '--', p], log, name=f'q4-{v}')
d.wait(lambda: d.count('PreToolUse') > 0, 60, 'PreToolUse'); time.sleep(3)
tree = d.tree(); print('tree while the tool runs:'); [print('   ', r) for r in tree]
kids = [r[0] for r in tree if r[0] != d.pid]
t = time.time()
if v == 'sigterm': d.end_sigterm()
elif v == 'hup': d.end_close_master()
elif v == 'sigkill': os.kill(d.pid, 9); d.mark('sent SIGKILL')
else: d.end_exit()
print('reap:', d.reap(60 if v == 'exit' else 15), 'in', round(time.time() - t, 2), 's'); time.sleep(2)
alive = subprocess.run(['ps', '-o', 'pid=,ppid=,pgid=,stat=,command=', '-p', ','.join(map(str, kids))], capture_output=True, text=True).stdout.strip() if kids else ''
print('former children still alive:', alive or 'none')
for k in kids:   # clean up whatever survived (only our own former children)
    try: os.kill(k, 9)
    except ProcessLookupError: pass
if not d.closed: d.dump('end')
report(d, f'q4-{v}'); d.save('run'); d.kill()
