#!/usr/bin/env python3
"""Spike 36: one scenario's logs, digested in time order: the driver's steps; the permission prompts (the settings
hook's PermissionRequest, the mod's tool.check verdicts, each call's end); the band's gaps (when it was not drawn);
the mod's actions and how each ended; compactions, boundaries and rewakes; prompts and turns; notifications, stops
and the agents' list.

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
mod.sort(key=lambda r: r['t'])
names = {r['r']['agentId']: r['name'] for r in mod if r['kind'] == 'spawned' and r.get('r', {}).get('agentId')}
who = lambda a: names.get(a, (a or '')[:8]) if a else 'lead'
cmd = lambda i: ((i or {}).get('command') or '')[:60]
cmds = {r.get('id'): r.get('command') for r in mod if r['kind'] == 'tool.check'}  # tool.call's event has no input
rows = []
for r in recs(os.path.join(work, 'hooks.jsonl')):
    i, ev, t = r.get('input', {}), r['ev'], float(r['t'])
    if ev == 'DRIVER':
        rows.append((t, '==', i.get('step', '')))
    elif ev in ('BOUNDARY', 'rewake'):
        rows.append((t, 'hook', ev + ' ' + json.dumps({k: v for k, v in r.items() if k in ('waited_ms', 'proof', 'out')} or i)[:200]))
    elif ev in ('PreCompact', 'PostCompact') or (ev == 'SessionStart' and i.get('source') in ('compact', 'clear')):
        rows.append((t, 'hook', f"{ev} {who(i.get('agent_id'))} trigger={i.get('trigger')} source={i.get('source')}"))
    elif ev == 'UserPromptSubmit':
        rows.append((t, 'hook', f"UserPromptSubmit {who(i.get('agent_id'))} {(i.get('prompt') or '')[:110]!r}"))
    elif ev == 'PermissionRequest':
        rows.append((t, 'hook', f"PermissionRequest {who(i.get('agent_id'))} type={i.get('agent_type')!r} {i.get('tool_name')} {cmd(i.get('tool_input'))!r}"))
    elif ev in ('PermissionDenied', 'PostToolUseFailure'):
        rows.append((t, 'hook', f"{ev} {who(i.get('agent_id'))} {i.get('tool_name')} {cmd(i.get('tool_input'))!r} {str(i.get('error') or i.get('reason') or '')[:100]!r}"))
    elif ev == 'PostToolUse' and i.get('tool_name') == 'Bash':
        rows.append((t, 'hook', f"PostToolUse {who(i.get('agent_id'))} Bash {cmd(i.get('tool_input'))!r}"))
    elif ev == 'Notification':
        rows.append((t, 'hook', f"Notification {who(i.get('agent_id'))} {i.get('notification_type')} {(i.get('message') or '')[:100]!r}"))
    elif ev in ('Stop', 'StopFailure', 'SubagentStop', 'SessionEnd', 'SubagentStart') and not (ev == 'Stop' and i.get('agent_id')):
        rows.append((t, 'hook', f"{ev} {who(i.get('agent_id'))} {i.get('reason') or i.get('error') or ''}"))
prev = None
for r in mod:
    k, t = r['kind'], r['t'] / 1000
    if k == 'render':
        if prev is not None and t - prev > 2.5:
            rows.append((prev, 'mod', f'band: not drawn for {t - prev:.1f}s from here'))
        prev = t
        v = r.get('view') or {}
        if v.get('agentId'):
            rows.append((t, 'mod', f"band drawn under {who(v.get('agentId'))}'s view"))
    elif k == 'tool.check' and r.get('decision') != 'allow':
        rows.append((t, 'mod', f"tool.check {who(r.get('agentId'))} {r.get('tool')} {r.get('command')!r} → {r.get('decision')} decider={r.get('decider')} {r.get('reason')!r}"))
    elif k == 'call-end' and r.get('tool') in ('Bash',):
        rows.append((t, 'mod', f"call-end {who(r.get('agentId'))} {cmds.get(r.get('id'))!r} after {r.get('ms') / 1000:.1f}s isError={r.get('isError')} deny={r.get('deny')} {r.get('text')!r}"))
    elif k in ('act-start', 'compact-resolved', 'compact-rejected', 'submit-resolved', 'submit-rejected', 'read', 'act-failed', 'sent', 'send-failed',
               'stopped', 'asking', 'asked', 'ask-failed', 'spawned', 'spawn-failed', 'renderask-set', 'session.end'):
        d = {x: v for x, v in r.items() if x not in ('t', 'inst', 'kind')}
        rows.append((t, 'mod', k + ' ' + json.dumps(d)[:300]))
    elif k == 'turn.complete':
        e = r.get('e') or {}
        extra = {x: v for x, v in e.items() if x not in ('agentId', 'reason', 'isAborted', 'durationMs', 'answer', 'turnId', 'messages', 'usage')}
        rows.append((t, 'mod', f"turn.complete {who(r.get('agentId'))} {r.get('reason')} {(r.get('answer') or '')[:60]!r} {json.dumps(extra)[:200]}"))
    elif k == 'turn.start' and not r.get('agentId'):
        rows.append((t, 'mod', 'turn.start lead'))
    elif k == 'prompt.submit':
        o = r.get('origin') or {}
        rows.append((t, 'mod', f"prompt.submit origin={o.get('kind')} turnId={'yes' if r.get('turnId') else 'no'} {(r.get('text') or '')[:90]!r}"))
    elif k == 'session.compact':
        rows.append((t, 'mod', f"session.compact {r.get('phase')} {who(r.get('agentId'))} trigger={r.get('trigger')} skip={r.get('skip')}"))
    elif k == 'classic' and r.get('ev') in ('PermissionRequest', 'Notification', 'SubagentStop', 'SessionEnd'):
        rows.append((t, 'mod', f"classic {r.get('ev')} {who(r.get('agent_id'))} {r.get('tool') or r.get('type') or r.get('reason') or ''}"))
    elif k in ('list', 'listed', 'list-cmd'):
        rows.append((t, 'mod', k + ' ' + ' '.join(f"{a.get('name') or a.get('id', '')[:6]}:{a.get('status')}" for a in r.get('list', []))))
t0 = min(x[0] for x in rows) if rows else 0
for t, src, s in sorted(rows, key=lambda x: x[0]):
    print(f'{t - t0:8.2f} {src:4} {s}')
