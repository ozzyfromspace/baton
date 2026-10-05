"""Spike 15: does a project's statusLine.refreshInterval apply to the status line baton passes with --settings,
and does a status line that changes every second keep the screen from ever being still for 1.5s?"""
import json, os, shutil, sys, time
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'common'))
from scenario import Driver
here = os.path.dirname(os.path.abspath(__file__))
for variant, cmd in [('static', 'cat > /dev/null; echo flag'), ('ticking', 'cat > /dev/null; date +%H:%M:%S')]:
    work = os.path.join(here, 'work-' + variant); shutil.rmtree(work, ignore_errors=True); os.makedirs(os.path.join(work, '.claude'))
    json.dump({'statusLine': {'type': 'command', 'command': 'echo project', 'refreshInterval': 1}}, open(os.path.join(work, '.claude', 'settings.json'), 'w'))
    d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps({'statusLine': {'type': 'command', 'command': cmd, 'padding': 0}})])
    d.trust(); time.sleep(8)
    gaps = []; t = time.time(); prev = d.last_out
    while time.time() - t < 20:
        time.sleep(0.05)
        if d.last_out != prev:
            gaps.append(round(d.last_out - prev, 2)); prev = d.last_out
    d.type('/exit'); d.enter(); time.sleep(3); d.close()
    print(variant, 'screen writes in 20s idle:', len(gaps), 'longest quiet gap:', max(gaps) if gaps else '20+')
