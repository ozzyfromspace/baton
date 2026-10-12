#!/usr/bin/env python3
"""Spike 32: one scenario's settings hooks (hooks.jsonl) and mod log (mod.jsonl.*) as one timeline.

Usage: timeline.py <workdir> [filter]. Agents are shown by the names the mod gave them. Busy kinds (list, tick, the
mod's classic.* echo) are left out unless the filter names them.
"""
import glob, json, os, sys

work = sys.argv[1]
want = sys.argv[2] if len(sys.argv) > 2 else ''


def recs(p):
    try:
        return [json.loads(l) for l in open(p) if l.strip()]
    except FileNotFoundError:
        return []


mod = []
for p in sorted(glob.glob(os.path.join(work, 'mod.jsonl.*'))):
    mod += recs(p)
names = {}
for r in mod:
    if r['kind'] == 'spawned' and r.get('r', {}).get('agentId'):
        names[r['r']['agentId']] = r['name']


def who(aid):
    return names.get(aid, aid[:8]) if aid else 'lead'


rows = []
for r in recs(os.path.join(work, 'hooks.jsonl')):
    i = r.get('input', {})
    ev = r['ev']
    bits = []
    if ev == 'DRIVER':
        rows.append((float(r['t']), '==', i.get('step', '')))
        continue
    if ev in ('BOUNDARY', 'rewake'):
        rows.append((float(r['t']), 'hook', f"{ev} {json.dumps({k: v for k, v in r.items() if k not in ('t', 'hms', 'ev', 'input')} or i)[:300]}"))
        continue
    a = i.get('agent_id')
    for k in ('source', 'trigger', 'reason', 'tool_name', 'notification_type', 'error', 'spk_loop', 'stop_hook_active'):
        if i.get(k) not in (None, ''):
            bits.append(f'{k}={i[k]}')
    ti = i.get('tool_input')
    if isinstance(ti, dict):
        bits.append(json.dumps(ti.get('command') or ti.get('file_path') or ti.get('description') or '')[:70])
    if ev == 'UserPromptSubmit':
        bits.append(repr((i.get('prompt') or '')[:120]))
    if ev == 'Stop' and i.get('background_tasks') is not None:
        bits.append('bg=' + json.dumps(i.get('background_tasks'))[:300])
    if 'entries' in r:
        bits.append(f"entries={r['entries']} boundaries={r.get('boundaries')} last_is_boundary={r.get('last_is_boundary')}")
    if r.get('out'):
        bits.append('out=' + json.dumps(r['out'])[:100])
    if i.get('agent_type') not in (None, '', 'spk:phase'):
        bits.append('type=' + i['agent_type'])
    rows.append((float(r['t']), 'hook', f"{ev:<17} [{who(a)}] " + ' '.join(bits)))

busy = {'list', 'tick', 'classic'}
for r in mod:
    k = r['kind']
    if k in busy and k not in want:
        continue
    d = {x: y for x, y in r.items() if x not in ('t', 'kind', 'inst')}
    a = d.pop('agentId', None) or d.pop('agent_id', None)
    if k == 'list':
        s = ' '.join(f"{x.get('name') or x['id'][:6]}:{x['status']}" for x in d['list'])
    else:
        s = json.dumps(d)[:300]
    rows.append((r['t'] / 1000, 'mod', f"{k:<17} [{who(a)}] i{r['inst'][-4:]} {s}"))

rows.sort(key=lambda x: x[0])
t0 = rows[0][0] if rows else 0
for t, src, s in rows:
    if want and want not in s and want != src:
        continue
    print(f'{t - t0:8.2f} {src:<4} {s}')
