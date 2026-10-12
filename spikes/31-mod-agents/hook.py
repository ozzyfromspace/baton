#!/usr/bin/env python3
"""Spike 31 settings hook: log {ev, input}; give each subagent a codeword at SubagentStart, and block each
subagent's first stop with one more instruction, to see whether both reach a subagent a mod started."""
import json, os, sys, time
ev, log_path = sys.argv[1], sys.argv[2]
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


out = None
if ev == 'SubagentStart':
    out = {'hookSpecificOutput': {'hookEventName': 'SubagentStart', 'additionalContext': 'The codeword for this task is HERON.'}}
elif ev == 'SubagentStop' and os.environ.get('SPK_BLOCK_STOP') == '1':
    state = log_path + '.stops'
    seen = open(state).read().split() if os.path.exists(state) else []
    aid = inp.get('agent_id') or ''
    if aid and aid not in seen:
        open(state, 'a').write(aid + '\n')
        out = {'decision': 'block', 'reason': 'Before you finish: use the Write tool to create the file stop.txt in your current directory containing exactly the word ok. Then reply DONE again.'}
elif ev == 'WorktreeCreate':
    # baton's way: the worktree outside the checkout, on a branch of its own, made by the host and not by Claude Code.
    import subprocess
    repo = inp.get('cwd') or os.getcwd()
    name = inp.get('name') or 'x'
    path = os.path.join(os.path.dirname(repo), 'bwt-' + name)
    subprocess.run(['git', 'worktree', 'add', '-q', '-b', 'baton/spk/' + name, path, 'HEAD'], cwd=repo,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp), 'out': path}
    with open(log_path, 'a') as f:
        f.write(json.dumps(rec) + '\n')
    print(path)
    sys.exit(0)
elif ev == 'PreToolUse' and inp.get('tool_name') == 'Workflow' and os.environ.get('SPK_ALLOW_WORKFLOW') == '1':
    out = {'hookSpecificOutput': {'hookEventName': 'PreToolUse', 'permissionDecision': 'allow', 'permissionDecisionReason': 'spike allows workflows'}}

rec = {'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'input': shrink(inp), 'out': out}
with open(log_path, 'a') as f:
    f.write(json.dumps(rec) + '\n')
if out:
    print(json.dumps(out))
