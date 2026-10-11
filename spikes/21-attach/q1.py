import time, sys, json, vt
from drv import Drv
out = {}
d = Drv('proj1', ['claude', '--model', 'haiku'], 50, 160, name='q1')
d.wait_has('trustthisfolder', 40); time.sleep(0.8); d.key(b'\x1b[B'); time.sleep(0.4); d.key(b'\r'); d.mark('trusted')
d.wait_quiet(3, 40); d.mark('idle')
d.type('Reply with three lines: ALPHA-ONE, BRAVO-TWO, CHARLIE-THREE, one per line, nothing else.'); d.enter(); d.mark('prompt sent')
d.wait_has('CHARLIE-THREE', 90); d.wait_quiet(3, 60); d.mark('answered')
d.type('draft-xyz'); d.wait_quiet(1.5, 10); d.mark('draft typed')

CHECKS = ['ALPHA-ONE', 'BRAVO-TWO', 'CHARLIE-THREE', 'Reply with three lines', 'draft-xyz', '───', 'Claude Code']
def judge(label, t, rows, cols):
    time.sleep(3.0); d.wait_quiet(1.5, 8)
    b = d.bytes_between(t)
    first = next((tt for tt, c in d.chunks if tt >= t), None)
    s = vt.Screen(rows, cols); s.feed(b)
    txt = s.text()
    full = '\n'.join(s.scrollback + [txt])
    res = {c: (c in txt) for c in CHECKS}
    res_sb = {c: (c in full) for c in CHECKS}
    open(f'B-{label}.txt', 'w').write(f'--- scrollback ({len(s.scrollback)} lines)\n' + '\n'.join(s.scrollback) + '\n--- screen\n' + txt)
    out[label] = dict(bytes=len(b), events=s.events, on_screen=res, incl_scrollback=res_sb,
                      nonblank_rows=sum(1 for l in txt.split('\n') if l.strip()), has_2026=b'\x1b[?2026h' in b,
                      has_ED2=b'\x1b[2J' in b, has_ED3=b'\x1b[3J' in b, has_home=b'\x1b[H' in b or b'\x1b[1;1H' in b, first_byte_ms=(round((first-t)*1000) if first else None))
    print(label, json.dumps(out[label]), flush=True)

import signal, os
# (b) same size + SIGWINCH
t = time.time(); d.mark('trigger b: same size + SIGWINCH'); d.setsize(50, 160); judge('b_same_size', t, 50, 160)
time.sleep(4)
# (e) bare SIGWINCH with no ioctl
t = time.time(); d.mark('trigger e: bare SIGWINCH'); os.kill(d.pid, signal.SIGWINCH); judge('e_bare_sigwinch', t, 50, 160)
time.sleep(4)
# (a) different size
t = time.time(); d.mark('trigger a: 120x40'); d.setsize(40, 120); judge('a_resize_120x40', t, 40, 120)
time.sleep(4)
# (d) size change then back
t = time.time(); d.mark('trigger d: 150x45 then 160x50'); d.setsize(45, 150); time.sleep(0.15); d.setsize(50, 160); judge('d_change_and_back', t, 50, 160)
time.sleep(4)
# (c) Ctrl-L
t = time.time(); d.mark('trigger c: Ctrl-L'); d.key(b'\x0c'); judge('c_ctrl_l', t, 50, 160)
time.sleep(1)
# ground-truth final screen (full replay, resizes applied at marks)
s = vt.Screen(50, 160)
resizes = {'trigger a: 120x40': (40,120), 'trigger d: 150x45 then 160x50': (50,160)}
mi = 0
for tt, c in d.chunks:
    while mi < len(d.marks) and d.marks[mi][0] <= tt:
        if d.marks[mi][1] in resizes: s.resize(*resizes[d.marks[mi][1]])
        mi += 1
    s.feed(c)
open('A-final.txt','w').write(s.text())
out['alt_screen_ever'] = any(b'\x1b[?1049h' in c for _, c in d.chunks)
out['ED3_ever'] = [round(t-d.t0,1) for t, c in d.chunks if b'\x1b[3J' in c]
print('alt_screen_ever', out['alt_screen_ever'], 'ED3 at', out['ED3_ever'])
d.save('q1.json'); json.dump(out, open('q1-results.json','w'), indent=1)
d.close()
