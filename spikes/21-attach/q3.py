import time, json, vt, os, sys, subprocess
from drv import Drv
tag = sys.argv[1]; pause = float(sys.argv[2])
HOOK = os.path.abspath('hook.sh'); log = f'hooks-{tag}.log'
if os.path.exists(log): os.remove(log)
hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'BATON_SPIKE={tag} {HOOK} {ev}'}]}] for ev in ('UserPromptSubmit','PreToolUse','PostToolUse','Stop')}
proj = f'proj-{tag}'; os.makedirs(proj, exist_ok=True)
d = Drv(proj, ['claude', '--model', 'haiku', '--allowedTools', 'Bash', '--settings', json.dumps({'hooks': hooks})], 50, 160, name=tag)
d.wait_has('trustthisfolder', 40); time.sleep(0.8); d.key(b'\x1b[B'); time.sleep(0.4); d.key(b'\r')
d.wait_quiet(3, 40)
d.type('Do these in order. 1) Run `date +%s > a.txt` with Bash. 2) Print the integers 1 to 500, one per line, plain text. 3) Run `date +%s > b.txt` with Bash. 4) Print the integers 501 to 900, one per line. 5) Run `date +%s > c.txt` with Bash. Then say FINISHED-MARK.'); d.enter()
t_sent = time.time(); d.mark('prompt sent')
stats = []
if pause:
    time.sleep(2); d.paused.set(); d.mark(f'pause reads {pause}s')
    end = time.time() + pause
    while time.time() < end:
        st = subprocess.run(['ps','-o','stat=,pcpu=','-p',str(d.pid)], capture_output=True, text=True).stdout.strip()
        stats.append((round(time.time()-t_sent,1), st)); time.sleep(3)
    d.paused.clear(); d.mark('resume reads')
d.wait(lambda: os.path.exists(log) and 'Stop' in open(log).read(), 240, 'Stop'); d.mark('Stop')
time.sleep(2); d.wait_quiet(2, 20)
ev = [(round(float(a)-t_sent,1), b) for a,b in (l.split() for l in open(log))]
files = {f: round(int(open(os.path.join(proj,f)).read().strip()) - t_sent, 1) for f in ('a.txt','b.txt','c.txt') if os.path.exists(os.path.join(proj,f))}
print(json.dumps(dict(tag=tag, pause=pause, hooks=ev, file_times=files, ps_during_pause=stats, finished=d.has('FINISHED-MARK'), alive=d.alive)))
d.save(f'{tag}.json'); d.close()
