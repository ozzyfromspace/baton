"""Spike 44: statusLine.refreshInterval on Claude Code 2.1.318, for baton's own status line (v0.8.20 P1).

For one variant (argv[1]), starts claude with a status line passed by --settings, as baton does, leaves it idle
for 60 seconds, and counts the status line command's runs and the screen's writes, with the longest quiet gap.
argv[2] is where the scratch projects go (default: beside this script).

  static     --settings refreshInterval 10, a status line whose text never changes
  ticking    --settings refreshInterval 10, a status line whose text changes every run
  none       --settings with no refreshInterval (baton before v0.8.20), static text
  project30  --settings refreshInterval 10, the project's settings refreshInterval 30
  project5   --settings refreshInterval 10, the project's settings refreshInterval 5
"""
import json, os, shutil, sys, time
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'common'))
from scenario import Driver

IDLE = 60
variant = sys.argv[1]
root = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(os.path.abspath(__file__))
work = os.path.join(root, 'work-' + variant)
shutil.rmtree(work, ignore_errors=True)
os.makedirs(os.path.join(work, '.claude'))
runs = os.path.join(work, 'runs.log')
text = {'ticking': 'date +%H:%M:%S'}.get(variant, 'echo "baton · P1 · static"')
cmd = f'date +%s >> "{runs}"; cat > /dev/null; {text}'
flag = {'type': 'command', 'command': cmd, 'padding': 0}
if variant != 'none':
    flag['refreshInterval'] = 10
project = {'project30': 30, 'project5': 5}.get(variant)
if project:
    json.dump({'statusLine': {'type': 'command', 'command': 'echo project', 'refreshInterval': project}},
              open(os.path.join(work, '.claude', 'settings.json'), 'w'))

d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps({'statusLine': flag})])
d.trust()
d.wait_quiet(3, 60)
open(runs, 'w').close()  # count from the start of the idle stretch
gaps = []
t = time.time()
prev = d.last_out
while time.time() - t < IDLE:
    time.sleep(0.05)
    if d.last_out != prev:
        gaps.append(round(d.last_out - prev, 2))
        prev = d.last_out
tail = round(time.time() - prev, 2)
n = sum(1 for _ in open(runs))
d.type('/exit'); d.enter(); time.sleep(3); d.close()
print(json.dumps({'variant': variant, 'idle_seconds': IDLE, 'command_runs': n, 'screen_writes': len(gaps),
                  'gaps': gaps, 'quiet_at_end': tail}))
