import json, sys, datetime as dt, collections
from tparse import P
name, commits_f, evf = sys.argv[1:4]
d = json.load(open(name + '.json'))
start = P(d['start'])
lab = []
for k, n, l in d['runs']: lab.extend([l]*n)
N = len(lab)
ev = [json.loads(l) for l in open(evf)]
st = {}; dn = {}
for e in ev:
    if e['kind'] == 'phase_started': st.setdefault(e['phase'], P(e['ts']))
    if e['kind'] == 'phase_done': dn[e['phase']] = P(e['ts'])
st.setdefault('P0', start)
commits = []
cur = None
for line in open(commits_f):
    line = line.rstrip('\n')
    if line.startswith('C '):
        _, iso, h, *subj = line.split(' ')
        t = dt.datetime.fromisoformat(iso).astimezone(dt.timezone.utc).replace(tzinfo=None)
        cur = dict(t=t, h=h, s=' '.join(subj), files=[]); commits.append(cur)
    elif line.strip() and cur: cur['files'].append(line.strip())
commits = sorted([c for c in commits if not c['s'].startswith('baton snapshot')], key=lambda c: c['t'])
def is_code(c): return any(not (f.startswith('docs/') or f.endswith('.md')) for f in c['files'])
WAIT = ('fg-gate', 'fg-poll', 'idle-bg-work')
out = {}
tot = collections.Counter()
print(f"{'ph':4s} {'wall':>5s} {'tail':>5s} {'tailwait':>8s} {'innerwait':>9s}  last-code-commit")
for p in sorted(dn, key=lambda p: int(p[1:])):
    a, b = st[p], dn[p]
    cs = [c for c in commits if a <= c['t'] <= b]
    code = [c for c in cs if is_code(c)]
    wc = (code or cs or [dict(t=a, s='(none)')])[-1]
    i0, i1, i2 = [max(0, min(N, int((x - start).total_seconds()))) for x in (a, wc['t'], b)]
    tw = sum(1 for k in range(i1, i2) if lab[k] in WAIT)
    iw = sum(1 for k in range(i0, i1) if lab[k] in WAIT)
    out[p] = dict(wall=i2 - i0, work_complete=i1 - i0, tail=i2 - i1, tail_wait=tw, inner_wait=iw,
                  labels=dict(collections.Counter(lab[i0:i2])), tail_labels=dict(collections.Counter(lab[i1:i2])))
    tot['wall'] += i2 - i0; tot['tail'] += i2 - i1; tot['tw'] += tw; tot['iw'] += iw
    print(f"{p:4s} {(i2-i0)/60:5.0f} {(i2-i1)/60:5.0f} {tw/60:8.0f} {iw/60:9.0f}  {wc['s'][:80]}")
print('totals h:', {k: round(v/3600, 1) for k, v in tot.items()})
json.dump(out, open(name + '-tails.json', 'w'), indent=1)
