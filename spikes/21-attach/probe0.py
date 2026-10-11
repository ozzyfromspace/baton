import time, sys, vt
from drv import Drv
d = Drv('proj1', ['claude', '--model', 'haiku'], 50, 160, name='p0')
d.wait_quiet(3, 40)
s = vt.Screen(50, 160)
for _, c in d.chunks: s.feed(c)
print(s.text())
print('EVENTS', s.events, 'modes', {k:v for k,v in s.modes.items()})
d.save('probe0.json')
d.close()
