import time, json, vt, os, signal
from drv import Drv
HOOK = os.path.abspath('hook.sh')
open(HOOK,'w').write('#!/bin/sh\npython3 -c "import time,sys;print(round(time.time(),3), sys.argv[1])" "$1" >> "$(dirname "$0")/hooks-$BATON_SPIKE.log"\n'); os.chmod(HOOK, 0o755)
hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'BATON_SPIKE=q2 {HOOK} {ev}'}]}] for ev in ('UserPromptSubmit','PreToolUse','PostToolUse','Stop')}
d = Drv('proj2', ['claude', '--model', 'haiku', '--settings', json.dumps({'hooks': hooks})], 50, 160, name='q2')
d.wait_has('trustthisfolder', 40); time.sleep(0.8); d.key(b'\x1b[B'); time.sleep(0.4); d.key(b'\r')
d.wait_quiet(3, 40); d.mark('idle')
d.type('Print the integers from 1 to 400, one per line, as plain text with no code block and nothing else. The last line must be 400.'); d.enter(); t_prompt = time.time(); d.mark('prompt sent')
# resize while streaming
d.wait(lambda: d.has('\n37\n') or d.has('37'), 60, 'streaming started'); d.mark('streaming')
sizes = [(40,120),(50,160),(30,100),(45,140),(50,160)]
for r,c in sizes:
    time.sleep(0.6); d.setsize(r,c); d.mark(f'resize {c}x{r}')
d.wait(lambda: 'Stop' in open('hooks-q2.log').read() if os.path.exists('hooks-q2.log') else False, 120, 'Stop hook'); d.mark('Stop seen')
time.sleep(3); d.wait_quiet(2, 20)
# attach after long content: Ctrl-L
t = time.time(); d.mark('ctrl-L'); d.key(b'\x0c'); time.sleep(3); d.wait_quiet(1.5, 8)
b = d.bytes_between(t)
s = vt.Screen(50,160); s.feed(b)
open('B-q2-ctrl_l.txt','w').write(s.text())
# ground truth via full replay with resizes
g = vt.Screen(50,160); mi = 0
rz = {f'resize {c}x{r}': (r,c) for r,c in sizes}
for tt, c in d.chunks:
    while mi < len(d.marks) and d.marks[mi][0] <= tt:
        if d.marks[mi][1] in rz: g.resize(*rz[d.marks[mi][1]])
        mi += 1
    g.feed(c)
open('A-q2.txt','w').write(g.text())
res = dict(repaint_bytes=len(b), total_bytes=sum(len(c) for _,c in d.chunks), same=(s.text()==g.text()),
           ED3=[round(tt-d.t0,1) for tt,c in d.chunks if b'\x1b[3J' in c], ED2=[round(tt-d.t0,1) for tt,c in d.chunks if b'\x1b[2J' in c],
           resize_bytes={lab: len(d.bytes_between(mt, mt+1.0)) for mt, lab in d.marks if lab.startswith('resize')})
print(json.dumps(res, indent=1))
# also: what does a full repaint after a SIZE change contain once the history is long?
t = time.time(); d.mark('resize 150x48 after long'); d.setsize(48,150); time.sleep(3); d.wait_quiet(1.5,8)
b2 = d.bytes_between(t); s2 = vt.Screen(48,150); s2.feed(b2)
open('B-q2-resize-after-long.txt','w').write('\n'.join(s2.scrollback)+'\n--- screen\n'+s2.text())
print('resize-after-long bytes', len(b2), 'scrollback lines', len(s2.scrollback), 'ED3' , b'\x1b[3J' in b2)
d.save('q2.json'); d.close()
