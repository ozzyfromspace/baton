#!/usr/bin/env python3
"""Stand-in for `baton elevate`: record this session for the shell to relaunch under the wrapper, then end claude cleanly."""
import json, os, signal, subprocess, sys, time
if os.environ.get('BATON'):
    print('baton: this session is already hosted by baton; nothing to do.'); sys.exit(0)
sid, cpid = os.environ.get('CLAUDE_CODE_SESSION_ID'), os.environ.get('CLAUDE_PID')
if not sid or not cpid:
    print(f'baton: cannot elevate, missing session id ({sid}) or claude pid ({cpid})'); sys.exit(1)
tty = subprocess.check_output(['ps', '-o', 'tty=', '-p', cpid], text=True).strip()
d = os.environ['ELEV_DIR']
pending = os.path.join(d, f'pending-{sid}.txt')
open(pending, 'w').write(sys.argv[1] if len(sys.argv) > 1 else 'Elevation complete. Continue.')
json.dump({'session_id': sid, 'cwd': os.getcwd(), 'claude_pid': int(cpid), 'pending_file': pending, 'created': time.time()},
          open(os.path.join(d, f'{tty}.json'), 'w'))
open(os.path.join(d, f'kill-{sid}'), 'w').write(cpid)
killer = f'''
import os, signal, time
d, sid, pid = {d!r}, {sid!r}, {int(cpid)}
log = open(os.path.join(d, 'killer.log'), 'a')
for _ in range(240):
    if os.path.exists(os.path.join(d, 'turn-ended-' + sid)): break
    time.sleep(0.5)
time.sleep(1.5)
os.kill(pid, signal.SIGTERM); log.write(time.strftime('%H:%M:%S') + ' sent SIGTERM\\n'); log.flush()
for _ in range(20):
    time.sleep(0.5)
    try: os.kill(pid, 0)
    except ProcessLookupError:
        log.write(time.strftime('%H:%M:%S') + ' claude exited after SIGTERM\\n'); break
else:
    os.kill(pid, signal.SIGKILL); log.write(time.strftime('%H:%M:%S') + ' had to SIGKILL\\n')
'''
subprocess.Popen([sys.executable, '-c', killer], start_new_session=True, stdin=subprocess.DEVNULL,
                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
print(f'baton: elevating session {sid[:8]} (tty {tty}). Claude will restart under baton when this turn ends; same conversation.')
