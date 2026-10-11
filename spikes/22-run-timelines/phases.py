import json,sys,datetime as dt
from tparse import P
def ts(s): return dt.datetime.fromisoformat(s.replace('Z','+00:00')[:26]+'+00:00' if '.' in s else s.replace('Z','+00:00'))
def _old(s):
    s=s.replace('Z','')
    if '.' in s:
        a,b=s.split('.'); s=a+'.'+b[:6]
    return dt.datetime.fromisoformat(s)
f=sys.argv[1]
ev=[json.loads(l) for l in open(f)]
ev.sort(key=lambda e:P(e['ts']))
start={};done={}
for e in ev:
    if e['kind']=='phase_started': start.setdefault(e['phase'],P(e['ts']))
    if e['kind']=='phase_done': done[e['phase']]=P(e['ts'])
att=[P(e['ts']) for e in ev if e['kind']=='attached']
comp=[e for e in ev if e['kind']=='plan_complete']
print('attached',att[:1],'complete',[c['ts'] for c in comp])
order=sorted(set(start)|set(done), key=lambda p:int(p[1:]))
tot=dt.timedelta()
for p in order:
    s=start.get(p); d=done.get(p)
    dur=(d-s) if s and d else None
    if dur: tot+=dur
    print(p, s, d, dur)
print('sum of phase windows', tot)
