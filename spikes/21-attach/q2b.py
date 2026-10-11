import time, json, vt, os, sys
from drv import Drv
tag = sys.argv[1]; do_resize = sys.argv[2] == 'resize'
HOOK = os.path.abspath('hook.sh'); log = f'hooks-{tag}.log'
if os.path.exists(log): os.remove(log)
hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'BATON_SPIKE={tag} {HOOK} {ev}'}]}] for ev in ('UserPromptSubmit','PreToolUse','PostToolUse','Stop')}
proj = f'proj-{tag}'; os.makedirs(proj, exist_ok=True)
d = Drv(proj, ['claude', '--model', 'haiku', '--allowedTools', 'Bash', '--settings', json.dumps({'hooks': hooks})], 50, 160, name=tag)
d.wait_has('trustthisfolder', 40); time.sleep(0.8); d.key(b'\x1b[B'); time.sleep(0.4); d.key(b'\r')
d.wait_quiet(3, 40)
d.type('First run the shell command `sleep 6` with the Bash tool. Then print the integers from 1 to 1500, one per line, as plain text, no code block, nothing else; the last line must be 1500.'); d.enter(); d.mark('prompt sent')
stopped = lambda: os.path.exists(log) and 'Stop' in open(log).read()
sizes = [(40,120),(50,160),(30,100),(45,140)]; i = 0; n = 0
while not stopped() and time.time() - d.t0 < 200:
    time.sleep(0.7)
    if do_resize:
        r,c = sizes[i % len(sizes)]; i += 1; d.setsize(r,c); n += 1
d.mark(f'Stop seen after {n} resizes')
d.setsize(50,160); time.sleep(3); d.wait_quiet(2, 20)
t = time.time(); d.key(b'\x0c'); time.sleep(3); d.wait_quiet(1.5, 8)
s = vt.Screen(50,160); s.feed(d.bytes_between(t))
txt = s.text(); open(f'B-{tag}.txt','w').write(txt)
L = [l.strip() for l in txt.split('\n') if l.strip()]
ev = [l.split() for l in open(log)]
t_up = float(next(x[0] for x in ev if x[1]=='UserPromptSubmit')); t_stop = float(next(x[0] for x in ev if x[1]=='Stop'))
nums = [int(l.lstrip('⏺ ').strip()) for l in L if l.lstrip('⏺ ').strip().isdigit()]
print(json.dumps(dict(tag=tag, resizes=n, turn_secs=round(t_stop-t_up,1), hooks=[x[1] for x in ev], last_numbers=nums[-3:], frame_ok=any('❯' in l for l in L) and any('──' in l for l in L),
     contiguous=all(b-a==1 for a,b in zip(nums,nums[1:])), alive=d.alive)))
d.save(f'{tag}.json'); d.close()
