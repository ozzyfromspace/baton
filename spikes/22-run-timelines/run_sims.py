import json, sys, math
from sim import simulate, critical_path
INF = math.inf
def load(dagfile, campaign=None):
    d = json.load(open(dagfile))
    if 'runs' in d: d = next(r for r in d['runs'] if r['campaign'] == campaign)
    return d['phases']
def graphs(phases, finish_all=None, weak=True):
    hard, gate = {}, {}
    for p in phases:
        for e in p['deps']:
            if e['kind'] == 'hard' and (weak or e.get('strength', 'strong') != 'weak'):
                hard.setdefault(p['id'], set()).add(e['on'])
            if e['kind'] == 'gate':
                gate.setdefault(p['id'], set()).add(e['on'])
    if finish_all:
        hard[finish_all] = {p['id'] for p in phases if p['id'] != finish_all}
    return hard, gate
def h(s): return f'{s/3600:5.1f}h'
def report(name, segs, hard, gate, label):
    ph = sorted(segs, key=lambda p: int(p[1:]))
    chain = {ph[i]: {ph[i-1]} for i in range(1, len(ph))}
    actual, _ = simulate(segs, chain, N=1, L=1, hold=True)
    rows = []
    def run(desc, **kw):
        t, _ = simulate(segs, hard, gate, **kw)
        rows.append((desc, t))
    run('A  parallel sessions, each waits on its gates (hold), N=inf, L=inf', N=INF, L=INF, hold=True)
    run('B  parallel sessions, hold, N=inf, ONE verification stack', N=INF, L=1, hold=True)
    run('C  one agent that never waits, ONE separate verification lane', N=1, L=1, hold=False)
    run('D  one agent never waits, L=2', N=1, L=2, hold=False)
    run('E  2 sessions never wait, L=1', N=2, L=1, hold=False)
    run('F  2 sessions never wait, L=2', N=2, L=2, hold=False)
    run('G  3 sessions never wait, L=2', N=3, L=2, hold=False)
    run('H  3 sessions never wait, L=3', N=3, L=3, hold=False)
    run('I  unlimited sessions never wait, L=inf (pure DAG bound)', N=INF, L=INF, hold=False)
    run('J  I + pipelined (dependents start at predecessor last code)', N=INF, L=INF, hold=False, pipelined=True)
    print(f'== {name} [{label}]  actual {h(actual)}  critical path (hold) {h(critical_path(segs, hard))}')
    for desc, t in rows: print(f'   {desc:72s} {h(t)}  {actual/t:4.2f}x')
for name, dagf, camp, finish in [('craft', 'dag-craft.json', None, 'P16'), ('attaform', 'dag-small.json', 'attaform', 'P5'), ('baton', 'dag-small.json', 'baton-v0.2', 'P7')]:
    segs = json.load(open(f'{name}-segs.json'))
    phases = load(dagf, camp)
    for weak in (False, True):
        hard, gate = graphs(phases, finish, weak)
        report(name, segs, hard, gate, 'strong+weak deps' if weak else 'strong deps only')
