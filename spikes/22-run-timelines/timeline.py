"""Label every second of a baton run's wall clock from the transcript + events.jsonl.
Usage: timeline.py <transcript.jsonl> <events.jsonl> <out.json>"""
import json, re, sys, collections
from tparse import P
tr, evf, out = sys.argv[1:4]

def text_of(c):
    if isinstance(c, str): return c
    if isinstance(c, list):
        return ' '.join((y.get('text','') if isinstance(y,dict) else str(y)) for y in c)
    return str(c)

GATE = re.compile(r'\b(make\s+(test|lint|check|e2e|typecheck|preflight|ci|build|gates?)[\w-]*|pytest|playwright|vitest|jest|go\s+test|npm\s+(run\s+)?(test|build|lint)|pnpm\s+(run\s+)?(test|build|lint|typecheck)|yarn\s+(test|build|lint)|nuxi\s+(typecheck|build)|tsc\b|vue-tsc|ruff|mypy|eslint|docker\s+compose\s+(up|build|run)|bin/dc\s+(up|build|run|exec)|test-[\w-]+)', re.I)
SLEEP = re.compile(r'(\bsleep\s+\d|\buntil\b|\bwhile\b.*\bdo\b|\bwait\b\s*$|\btail\s+-f|timeout\s+\d)', re.I)
def classify(cmd):
    if re.search(r'^\s*baton\s', cmd): return 'baton'
    g = GATE.search(cmd); s = SLEEP.search(cmd)
    if s and not g: return 'poll'
    if g and s: return 'gate+poll'
    if g: return 'gate'
    return 'other'

recs = []
uses = {}         # tool_use_id -> (ts, name, input)
bg = {}           # task id -> {start, end, cmd, kind}
fg = []           # (start, end, name, class, cmd)
turn_start = None; turns = []   # (start, end)
compacts = []
wakeups = []
with open(tr) as fh:
    for l in fh:
        try: d = json.loads(l)
        except: continue
        if d.get('isSidechain'): continue
        t = d.get('timestamp')
        if not t: continue
        t = P(t); typ = d.get('type'); m = d.get('message')
        if typ == 'system':
            st = d.get('subtype')
            if st in ('stop_hook_summary', 'turn_duration'):
                if turn_start: turns.append((turn_start, t)); turn_start = None
            if st == 'compact_boundary':
                cm = d.get('compactMetadata', {})
                dur = (cm.get('durationMs') or 0)/1000
                compacts.append((t, dur, cm.get('trigger'), cm.get('preTokens')))
            continue
        if typ == 'assistant' and isinstance(m, dict):
            if turn_start is None: turn_start = t
            for x in m.get('content') or []:
                if x.get('type') == 'tool_use':
                    uses[x['id']] = (t, x['name'], x.get('input', {}))
                    if x['name'] == 'ScheduleWakeup': wakeups.append((t, x['input'].get('delaySeconds')))
        elif typ == 'user' and isinstance(m, dict):
            c = m.get('content')
            if isinstance(c, str) or (isinstance(c, list) and any(x.get('type')=='text' for x in c if isinstance(x,dict)) and not any(x.get('type')=='tool_result' for x in c if isinstance(x,dict))):
                s = text_of(c)
                if '<task-notification>' in s:
                    tid = re.search(r'<task-id>([^<]+)</task-id>', s)
                    stt = re.search(r'<status>([^<]+)</status>', s)
                    if tid:
                        b = bg.setdefault(tid.group(1), {'cmd': '?', 'kind': '?'})
                        if stt and stt.group(1) in ('completed','failed','killed','stopped'): b.setdefault('end', t)
                        b.setdefault('first_note', t)
                if turn_start is None and not d.get('isMeta'): turn_start = t
            if isinstance(c, list):
                for x in c:
                    if isinstance(x, dict) and x.get('type') == 'tool_result':
                        u = uses.get(x.get('tool_use_id'))
                        if not u: continue
                        st0, name, inp = u
                        res = text_of(x.get('content'))
                        mm = re.search(r'running in background with ID: (\w+)', res) or re.search(r'(?:task|monitor|agent)[ _-]?id[":\s]+(\w{6,})', res, re.I)
                        if (inp.get('run_in_background') or name in ('Monitor',) ) and mm:
                            cmd = inp.get('command') or inp.get('description') or ''
                            bg.setdefault(mm.group(1), {}).update(start=st0, cmd=cmd[:2000], kind=name if name!='Bash' else classify(cmd))
                        elif name == 'Agent' and inp.get('run_in_background') and mm:
                            bg.setdefault(mm.group(1), {}).update(start=st0, cmd=inp.get('description',''), kind='agent')
                        else:
                            cmd = inp.get('command', '') if name == 'Bash' else ''
                            cls = classify(cmd) if name == 'Bash' else name
                            fg.append((st0, t, name, cls, (cmd or json.dumps(inp))[:2000]))

ev = [json.loads(l) for l in open(evf)]
for e in ev: e['t'] = P(e['ts'])
ev.sort(key=lambda e: e['t'])
start = min(e['t'] for e in ev if e['kind'] in ('attached',))
end = max(e['t'] for e in ev if e['kind'] == 'plan_complete')
N = int((end - start).total_seconds()) + 1
def idx(t): return max(0, min(N, int((t - start).total_seconds())))

# label arrays with priority (higher wins)
PRI = {'idle-other':0,'idle-bg-work':1,'idle-human':2,'idle-error':3,'turn':4,'fg-other':5,'fg-poll':6,'fg-gate':7,'fg-agent':6,'fg-human':7,'compact':8}
lab = ['idle-other']*N
def paint(a, b, name):
    i, j = idx(a), idx(b)
    for k in range(i, j):
        if PRI[name] >= PRI[lab[k]]: lab[k] = name
# background work spans (model's own async work)
for tid, b in bg.items():
    if 'start' in b:
        e2 = b.get('end') or b.get('first_note')
        if e2 and b.get('kind') not in ('Monitor',):
            paint(b['start'], e2, 'idle-bg-work')
# human waits from events: escalated/blocked/paused/dialog/proposal until resolution
open_h = None
for e in ev:
    k = e['kind']
    if k in ('escalated','paused','proposal_asked','dialog_waiting','review_due') and open_h is None: open_h = e['t']
    if k in ('resumed','proposal_resolved','review_resolved','turn_started') and open_h is not None:
        if k != 'turn_started' or e.get('by') == 'human':
            paint(open_h, e['t'], 'idle-human'); open_h = None
    if k == 'stop_failure':
        nxt = next((x['t'] for x in ev if x['t'] > e['t'] and x['kind']=='turn_started'), e['t'])
        paint(e['t'], nxt, 'idle-error')
for a, b in turns: paint(a, b, 'turn')
for a, b, name, cls, cmd in fg:
    if (b - a).total_seconds() < 1: continue
    if name == 'Agent': paint(a, b, 'fg-agent')
    elif name == 'AskUserQuestion': paint(a, b, 'fg-human')
    elif name == 'Bash' and cls in ('gate','gate+poll'): paint(a, b, 'fg-gate')
    elif name == 'Bash' and cls == 'poll': paint(a, b, 'fg-poll')
    else: paint(a, b, 'fg-other')
for e in ev:
    if e['kind'] == 'compact_started':
        f = next((x['t'] for x in ev if x['t'] >= e['t'] and x['kind']=='compact_finished'), None)
        if f: paint(e['t'], f, 'compact')

# per phase aggregation
phs = {}; cur = 'P0'; bounds = []
for e in ev:
    if e['kind'] == 'phase_started': bounds.append((e['t'], e['phase']))
def phase_at(k):
    t = start
    p = 'P0'
    return p
pa = ['P0']*N
for t, p in bounds:
    i = idx(t)
    for k in range(i, N): pa[k] = p
agg = collections.defaultdict(collections.Counter)
for k in range(N): agg[pa[k]][lab[k]] += 1
tot = collections.Counter(lab)
bgl = [ {**v, 'id':k, 'start':str(v.get('start')), 'end':str(v.get('end') or v.get('first_note')), 'secs': ((v.get('end') or v.get('first_note')) - v['start']).total_seconds() if v.get('start') and (v.get('end') or v.get('first_note')) else None} for k,v in bg.items()]
allfg = [dict(start=str(a), end=str(b), name=n, cls=c, cmd=cmd) for a,b,n,c,cmd in fg if (b-a).total_seconds() >= 10]
longfg = [dict(start=str(a), secs=(b-a).total_seconds(), name=n, cls=c, cmd=cmd) for a,b,n,c,cmd in fg if (b-a).total_seconds() >= 120]
runs=[]
for k in range(N):
    if runs and runs[-1][2]==lab[k]: runs[-1][1]+=1
    else: runs.append([k,1,lab[k]])
json.dump(dict(runs=runs, start=str(start), end=str(end), wall=N, totals=tot, phases={p: dict(c) for p,c in agg.items()},
               background=bgl, long_foreground=longfg, fg=allfg, compacts=[(str(a),b,c,d) for a,b,c,d in compacts], wakeups=[(str(a),b) for a,b in wakeups]), open(out,'w'), indent=1, default=str)
def h(s): return f'{s/3600:5.1f}h'
print('wall', h(N))
for k,v in tot.most_common(): print(f'  {k:14s} {h(v)}  {100*v/N:4.1f}%')
