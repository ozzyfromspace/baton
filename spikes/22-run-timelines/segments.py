"""Per phase: ordered segments [(type, kind, seconds)], type W (agent busy) or V (waiting on verification)."""
import json, re, sys, collections
from tparse import P
name, evf = sys.argv[1:3]
d = json.load(open(name + '.json'))
start = P(d['start'])
lab = []
for k, n, l in d['runs']: lab.extend([l]*n)
N = len(lab)
def idx(t): return max(0, min(N, int((P(t) - start).total_seconds())))

def gkind(cmd):
    c = cmd.lower()
    if re.search(r'test-(a11y|hover|tables|smoke|realtime|toplayer|escape|overlays|drives|design|print)|make\s+drive|playwright|drive|sweep|contact|shoot|seed|bin/dc|docker|health|a11y|hover|nuxt|frontend', c): return 'stack'
    if re.search(r'make\s+test(\s|$|[;>&|])|pytest|make-test|maketest|backend\.log|test-p\d', c): return 'backend'
    if re.search(r'lint|typecheck|tsc\b|vue-tsc|eslint|ruff|mypy|vitest|go\s+test|go\s+vet|make\s+(e2e|test)', c): return 'static'
    return 'other'

kind = [None]*N
for b in d['background']:
    if b.get('secs') and b.get('start') not in (None, 'None') and b.get('end') not in (None, 'None'):
        k = gkind(b.get('cmd', ''))
        for i in range(idx(b['start']), idx(b['end'])):
            if kind[i] in (None, 'other'): kind[i] = k
for f in d['fg']:
    if f['name'] != 'Bash': continue
    k = gkind(f['cmd'])
    for i in range(idx(f['start']), idx(f['end'])):
        if lab[i] in ('fg-gate', 'fg-poll') and (kind[i] in (None, 'other') or lab[i] == 'fg-gate'): kind[i] = k
WAIT = {'fg-gate', 'fg-poll', 'idle-bg-work'}
ev = [json.loads(l) for l in open(evf)]
st = {}; dn = {}
for e in ev:
    if e['kind'] == 'phase_started': st.setdefault(e['phase'], e['ts'])
    if e['kind'] == 'phase_done': dn[e['phase']] = e['ts']
st.setdefault('P0', d['start'])
out = {}
tot = collections.Counter()
for p in sorted(dn, key=lambda p: int(p[1:])):
    i0, i1 = idx(st[p]), idx(dn[p])
    segs = []
    for i in range(i0, i1):
        if lab[i] in WAIT: t, k = 'V', (kind[i] or 'other')
        else: t, k = 'W', lab[i]
        tot[(t, k if t == 'V' else 'W')] += 1
        if segs and segs[-1][0] == t and (t == 'W' or segs[-1][1] == k): segs[-1][2] += 1
        else: segs.append([t, k if t == 'V' else 'W', 1])
    # absorb blips < 60s into the previous segment to keep the structure readable
    merged = []
    for s in segs:
        if merged and (s[2] < 60 or (merged[-1][0] == s[0] and merged[-1][1] == s[1])):
            merged[-1][2] += s[2]
        else: merged.append(s)
    out[p] = merged
json.dump(out, open(name + '-segs.json', 'w'))
print(name, {f'{t}:{k}': round(v/3600, 1) for (t, k), v in sorted(tot.items())})
for p, s in out.items():
    W = sum(x[2] for x in s if x[0] == 'W')/60; V = collections.Counter()
    for x in s:
        if x[0] == 'V': V[x[1]] += x[2]/60
    print(f'  {p:4s} W={W:5.0f}m  V=' + ' '.join(f'{k}:{v:.0f}' for k, v in V.most_common()) + f'  segs={len(s)}')
