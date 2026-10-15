#!/usr/bin/env python3
"""Spike 32 settings hook: log {ev, input} to hooks.jsonl. As spike 31's: a codeword at SubagentStart, each
subagent's first stop blocked (SPK_BLOCK_STOP=1), baton's way of making a worktree at WorktreeCreate. New:

- at PreCompact, SessionStart(compact) and PostCompact, how many entries the lead's transcript has and whether a
  compact_boundary is among the last; after PostCompact a detached watcher logs when (if) a new boundary is written;
- `rewake`: the asyncRewake PostCompact hook, registered for every trigger: it waits (up to 20 s) for a new
  compact_boundary in the lead's transcript (the proof the compaction was the lead's), then wakes the model with a
  long brief (exit 2), or logs that no proof came and exits 0.
"""
import json, os, subprocess, sys, time

ev, log_path = sys.argv[1], sys.argv[2]


def now():
    return time.time()


def write(rec):
    rec.setdefault('t', f'{now():.3f}')
    rec.setdefault('hms', time.strftime('%H:%M:%S'))
    with open(log_path, 'a') as f:
        f.write(json.dumps(rec) + '\n')


def boundaries(tp):
    """(entries, [line numbers of compact_boundary entries]) of a transcript."""
    try:
        lines = open(tp).read().splitlines()
    except OSError:
        return 0, []
    out = []
    for i, l in enumerate(lines):
        if '"compact_boundary"' in l:
            try:
                if json.loads(l).get('subtype') == 'compact_boundary':
                    out.append(i + 1)
            except Exception:
                pass
    return len(lines), out


if ev == 'WATCH':  # detached: hook.py WATCH <log> <transcript> <t0> <boundaries at the hook>
    tp, t0, before = sys.argv[3], float(sys.argv[4]), int(sys.argv[5])
    end = now() + 30
    while now() < end:
        n, b = boundaries(tp)
        if len(b) > before:
            write({'ev': 'BOUNDARY', 'input': {'after_postcompact_ms': round((now() - t0) * 1000), 'entries': n, 'boundary_line': b[-1]}})
            sys.exit(0)
        time.sleep(0.02)
    write({'ev': 'BOUNDARY', 'input': {'none_within_s': 30, 'boundaries': before}})
    sys.exit(0)

raw = sys.stdin.read()
try:
    inp = json.loads(raw)
except Exception:
    inp = {'_raw': raw[:500]}


def shrink(v):
    if isinstance(v, str) and len(v) > 300:
        return v[:300] + f'…(+{len(v) - 300})'
    if isinstance(v, dict):
        return {k: shrink(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shrink(x) for x in v[:10]]
    return v


BRIEF = ('[spk] Context compacted. This is the brief for the next phase. ' + ' '.join(
    f'Rule {i}: keep the work committed, stage files by path, and never amend commits you did not make.' for i in range(60))
    + ' The brief\'s codeword is OSPREY. Reply with only that codeword.')

if ev == 'rewake':
    tp = inp.get('transcript_path') or ''
    t0 = now()
    n0, b0 = boundaries(tp)
    # The boundary may already be there (this hook can start after it is written): count from the lead
    # transcript's boundaries at the last PreCompact, which the synchronous hook leaves in a side file.
    base = len(b0)
    try:
        for l in open(log_path + '.seen'):
            r = json.loads(l)
            if r.get('session_id') == inp.get('session_id'):
                base = r['boundaries']
    except OSError:
        pass
    got = None
    while now() - t0 < 20:
        n, b = boundaries(tp)
        if len(b) > base:
            got = (n, b[-1])
            break
        time.sleep(0.05)
    rec = {'ev': 'rewake', 'input': shrink(inp), 'waited_ms': round((now() - t0) * 1000), 'base': base, 'at_start': [n0, b0[-1:]], 'proof': got}
    if got and os.environ.get('SPK_REWAKE') == '1':
        rec['out'] = 'exit 2 with the brief (%d chars)' % len(BRIEF)
        write(rec)
        sys.stderr.write(BRIEF)
        sys.exit(2)
    write(rec)
    sys.exit(0)

out = None
extra = {}
if ev in ('PreCompact', 'PostCompact') or (ev == 'SessionStart' and inp.get('source') == 'compact'):
    n, b = boundaries(inp.get('transcript_path') or '')
    extra = {'entries': n, 'boundaries': b[-3:], 'last_is_boundary': bool(b) and b[-1] == n}
    if ev == 'PreCompact':
        with open(log_path + '.seen', 'a') as f:
            f.write(json.dumps({'session_id': inp.get('session_id'), 'boundaries': len(b)}) + '\n')
    if ev == 'PostCompact':
        subprocess.Popen([sys.executable, os.path.abspath(__file__), 'WATCH', log_path, inp.get('transcript_path') or '', f'{now():.3f}', str(len(b))],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
if ev == 'SubagentStart':
    out = {'hookSpecificOutput': {'hookEventName': 'SubagentStart', 'additionalContext': 'The codeword for this task is HERON.'}}
elif ev == 'SubagentStop' and os.environ.get('SPK_BLOCK_STOP') == '1':
    state = log_path + '.stops'
    seen = open(state).read().split() if os.path.exists(state) else []
    aid = inp.get('agent_id') or ''
    if aid and aid not in seen and inp.get('agent_type') == 'spk:phase':
        open(state, 'a').write(aid + '\n')
        out = {'decision': 'block', 'reason': 'Before you finish: use the Write tool to create the file stop.txt in your current directory containing exactly the word ok. Then reply DONE again.'}
elif ev == 'WorktreeCreate':
    repo = inp.get('cwd') or os.getcwd()
    name = inp.get('name') or 'x'
    path = os.path.join(os.path.dirname(repo), 'bwt-' + name)
    if not os.path.isdir(path):
        subprocess.run(['git', 'worktree', 'add', '-q', '-b', 'baton/spk/' + name, path, 'HEAD'], cwd=repo,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    write({'ev': ev, 'input': shrink(inp), 'out': path})
    print(path)
    sys.exit(0)

write({'ev': ev, 'input': shrink(inp), 'out': out, **extra})
if out:
    print(json.dumps(out))
