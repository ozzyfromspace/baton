#!/usr/bin/env python3
"""Spike hook: append the full input of any event (plus the env claude gives hooks) to a JSONL log. Always exits 0."""
import json, os, sys, time
try:
    ev = sys.argv[1]; log = sys.argv[2]
    try:
        inp = json.load(sys.stdin)
    except Exception:
        inp = {}
    def shrink(v):
        if isinstance(v, str) and len(v) > 600: return v[:600] + f'...(+{len(v)-600})'
        if isinstance(v, dict): return {k: shrink(x) for k, x in v.items()}
        if isinstance(v, list): return [shrink(x) for x in v[:12]]
        return v
    keep = ('CLAUDE_CODE_MESSAGING_SOCKET', 'CLAUDE_CODE_MESSAGING_TOKEN', 'CLAUDE_JOB_DIR', 'CLAUDE_CODE_SESSION_ID',
            'CLAUDE_PID', 'CLAUDE_PROJECT_DIR', 'CLAUDE_CODE_ENTRYPOINT', 'CLAUDECODE', 'TERM', 'PWD', 'CLAUDE_CODE_SESSION_ATTENDED', 'CLAUDE_CODE_CHILD_SESSION', 'CLAUDE_EFFORT')
    rec = {'t': round(time.time(), 3), 'hms': time.strftime('%H:%M:%S'), 'ev': ev, 'pid': os.getpid(), 'ppid': os.getppid(),
           'hook_cwd': os.getcwd(), 'env': {k: os.environ.get(k) for k in keep if os.environ.get(k) is not None},
           'env_claude_keys': sorted(k for k in os.environ if 'CLAUDE' in k), 'input': shrink(inp)}
    with open(log, 'a') as f: f.write(json.dumps(rec) + '\n')
except Exception:
    pass
sys.exit(0)
