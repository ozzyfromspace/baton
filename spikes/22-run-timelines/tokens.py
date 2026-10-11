import json, sys, collections
from tparse import P
f = sys.argv[1]
seen = {}
with open(f) as fh:
    for l in fh:
        try: d = json.loads(l)
        except: continue
        if d.get('type') != 'assistant': continue
        m = d.get('message') or {}
        u = m.get('usage'); mid = m.get('id')
        if not u or not mid: continue
        seen[mid] = (P(d['timestamp']), u, m.get('model'), d.get('isSidechain'))
hours = collections.defaultdict(collections.Counter)
models = collections.Counter()
for t, u, model, side in seen.values():
    h = t.replace(minute=0, second=0, microsecond=0)
    c = hours[h]
    c['out'] += u.get('output_tokens', 0) or 0
    c['in'] += (u.get('input_tokens', 0) or 0)
    c['cw'] += u.get('cache_creation_input_tokens', 0) or 0
    c['cr'] += u.get('cache_read_input_tokens', 0) or 0
    c['calls'] += 1
    models[(model, bool(side))] += 1
hs = sorted(hours)
tot = collections.Counter()
for h in hs: tot.update(hours[h])
span = (hs[-1] - hs[0]).total_seconds()/3600 + 1
print('models', models.most_common(5))
print(f"calls {tot['calls']}  output {tot['out']/1e6:.2f}M  uncached-in {tot['in']/1e6:.2f}M  cache-write {tot['cw']/1e6:.1f}M  cache-read {tot['cr']/1e6:.0f}M over {span:.0f}h")
# rolling 5h windows
best = max(hs, key=lambda h0: sum(hours[h]['out'] for h in hs if 0 <= (h-h0).total_seconds() < 5*3600))
w = collections.Counter()
for h in hs:
    if 0 <= (h-best).total_seconds() < 5*3600: w.update(hours[h])
print(f"peak 5h window from {best}: calls {w['calls']} output {w['out']/1e6:.2f}M cache-read {w['cr']/1e6:.0f}M cache-write {w['cw']/1e6:.1f}M")
print(f"mean per hour: calls {tot['calls']/span:.0f} output {tot['out']/span/1e3:.0f}k cache-read {tot['cr']/span/1e6:.1f}M")
