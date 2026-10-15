#!/usr/bin/env python3
"""Spike 35: one scenario's summary.json and logs, digested: the mod's actions and how each ended, the compaction
hooks and boundaries, the rewakes, the prompts the lead received (origin, source) and every turn, in time order.

Usage: digest.py <workdir>
"""
import glob, json, os, sys

work = sys.argv[1]


def recs(p):
    try:
        return [json.loads(l) for l in open(p) if l.strip()]
    except FileNotFoundError:
        return []


mod = []
for p in sorted(glob.glob(os.path.join(work, 'mod.jsonl.*'))):
    mod += recs(p)
names = {r['r']['agentId']: r['name'] for r in mod if r['kind'] == 'spawned' and r.get('r', {}).get('agentId')}
who = lambda a: names.get(a, (a or '')[:8]) if a else 'lead'
rows = []
for r in recs(os.path.join(work, 'hooks.jsonl')):
    i, ev, t = r.get('input', {}), r['ev'], float(r['t'])
    if ev == 'DRIVER':
        rows.append((t, '==', i.get('step', '')))
    elif ev in ('BOUNDARY', 'rewake'):
        rows.append((t, 'hook', ev + ' ' + json.dumps({k: v for k, v in r.items() if k in ('waited_ms', 'proof', 'out')} or i)[:200]))
    elif ev in ('PreCompact', 'PostCompact') or (ev == 'SessionStart' and i.get('source') == 'compact'):
        rows.append((t, 'hook', f"{ev} {who(i.get('agent_id'))} trigger={i.get('trigger')} source={i.get('source')} entries={r.get('entries')} last_is_boundary={r.get('last_is_boundary')}"))
    elif ev == 'UserPromptSubmit':
        rows.append((t, 'hook', f"UserPromptSubmit {who(i.get('agent_id'))} source={i.get('source')!r} {(i.get('prompt') or '')[:110]!r}"))
    elif ev in ('Stop', 'StopFailure', 'SubagentStop', 'SessionEnd') and not (ev == 'Stop' and i.get('agent_id')):
        rows.append((t, 'hook', f"{ev} {who(i.get('agent_id'))} {i.get('reason') or ''}"))
for r in mod:
    k, t = r['kind'], r['t'] / 1000
    if k in ('act-start', 'compact-resolved', 'compact-rejected', 'submit-resolved', 'submit-rejected', 'read', 'filled', 'stashed',
             'restored', 'act-failed', 'sent', 'send-failed', 'asking', 'asked', 'ask-failed', 'spawned', 'spawn-failed'):
        d = {x: v for x, v in r.items() if x not in ('t', 'inst', 'kind')}
        rows.append((t, 'mod', k + ' ' + json.dumps(d)[:260]))
    elif k == 'turn.complete':
        rows.append((t, 'mod', f"turn.complete {who(r.get('agentId'))} {r.get('reason')} {r.get('answer')!r}"))
    elif k == 'turn.start' and not r.get('agentId'):
        rows.append((t, 'mod', 'turn.start lead'))
    elif k == 'prompt.submit':
        o = r.get('origin') or {}
        rows.append((t, 'mod', f"prompt.submit origin={o.get('kind')} sender={who(o.get('senderTaskId'))} handback={o.get('handback')} turnId={'yes' if r.get('turnId') else 'no'} ours={r.get('ours')} named={r.get('named')} {(r.get('text') or '')[:90]!r}"))
    elif k == 'session.compact':
        rows.append((t, 'mod', f"session.compact {r.get('phase')} {who(r.get('agentId'))} trigger={r.get('trigger')} skip={r.get('skip')}"))
    elif k == 'list' and any(a.get('status') not in ('running', 'idle') for a in r.get('list', [])):
        rows.append((t, 'mod', 'list ' + ' '.join(f"{a.get('name')}:{a.get('status')}" for a in r['list'])))
t0 = min(x[0] for x in rows) if rows else 0
for t, src, s in sorted(rows):
    print(f'{t - t0:8.2f} {src:4} {s}')
