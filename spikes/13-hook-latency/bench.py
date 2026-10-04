#!/usr/bin/env python3
"""Spike 13: start-up latency of a hook handler. Build first: CGO_ENABLED=0 go build -trimpath -o hookbin ."""
import subprocess, time, statistics, os
payload = b'{"session_id":"x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo hi"}}'
def bench(cmd, env, n=200):
    ts = []
    for _ in range(n):
        t = time.perf_counter(); subprocess.run(cmd, input=payload, env=env, stdout=subprocess.DEVNULL); ts.append((time.perf_counter() - t) * 1000)
    ts.sort(); return f'p50 {statistics.median(ts):.1f}ms  p95 {ts[int(n * 0.95)]:.1f}ms'
base = {k: v for k, v in os.environ.items() if not k.startswith('BATON')}
print('go binary, dormant           ', bench(['./hookbin', 'hook', 'PostToolUse'], base))
print('sh launcher, dormant (no Go) ', bench(['./launcher.sh', 'hook', 'PostToolUse'], base))
print('sh launcher -> go, hosted    ', bench(['./launcher.sh', 'hook', 'PostToolUse'], {**base, 'BATON_HOST': '1'}))
print('python3 hook (spike-style)   ', bench(['python3', '-c', 'import json,sys;json.load(sys.stdin)'], base, 50))
