import time, json, os, subprocess
from drv import Drv
ws = []
t0 = time.time()
for i in range(3):
    p = f'proj-q4-{i}'; os.makedirs(p, exist_ok=True)
    ws.append(Drv(p, ['claude', '--model', 'haiku'], 50, 160, name=f'w{i}'))
times = {}
for w in ws:
    w.wait_has('trustthisfolder', 60); times.setdefault(w.name, {})['trust_prompt_s'] = round(time.time()-w.t0, 1)
    time.sleep(0.5); w.key(b'\x1b[B'); time.sleep(0.3); w.key(b'\r'); ta = time.time()
    w.wait(lambda: w.has('shift+tab') or w.has('?forshortcuts'), 60, 'idle footer'); times[w.name]['ready_after_trust_s'] = round(time.time()-ta, 1)
time.sleep(20)
def tree(pid):
    out = subprocess.run(['ps','-A','-o','pid=,ppid=,rss=,pcpu=,comm='], capture_output=True, text=True).stdout.split('\n')
    rows = [l.split(None, 4) for l in out if l.strip()]
    kids = {pid}; changed = True
    while changed:
        changed = False
        for r in rows:
            if int(r[1]) in kids and int(r[0]) not in kids: kids.add(int(r[0])); changed = True
    return [r for r in rows if int(r[0]) in kids]
samples = []
for k in range(5):
    for w in ws:
        rs = tree(w.pid)
        samples.append(dict(w=w.name, k=k, rss_mb=round(sum(int(r[2]) for r in rs)/1024), cpu=round(sum(float(r[3]) for r in rs),1), procs=[r[4].split('/')[-1] for r in rs]))
    time.sleep(3)
print(json.dumps(times))
for s in samples[-3:]: print(json.dumps(s))
import statistics
print('mean idle cpu% per worker', round(statistics.mean(s['cpu'] for s in samples),2), 'mean rss MB per worker', round(statistics.mean(s['rss_mb'] for s in samples)))
for w in ws: w.close()
