#!/usr/bin/env python3
"""Spike 42: what does a transcript record of a usage-limit stop?

    python3 stops.py ~/.claude/projects/<project folder> [...]

Reads every transcript under the folders given (the lead's and its agents' subagents/agent-*.jsonl), finds
each entry Claude Code wrote for an API error of kind rate_limit, and prints only their shapes: a count per
Claude Code version, window kind (quotaLimits.rateLimitType), whether a reset is given, and the message with
its digits blanked. Nothing a transcript says beyond that is printed."""
import collections, glob, json, os, re, sys

shapes = collections.Counter()
for root in sys.argv[1:]:
    for path in glob.glob(os.path.join(os.path.expanduser(root), '**', '*.jsonl'), recursive=True):
        with open(path, errors='replace') as f:
            for line in f:
                if '"error":"rate_limit"' not in line:
                    continue
                try:
                    r = json.loads(line)
                except ValueError:
                    continue
                if r.get('error') != 'rate_limit' or not r.get('isApiErrorMessage'):
                    continue
                q = r.get('quotaLimits') or {}
                info = (r.get('apiErrorParams') or {}).get('rate_limit_info') or {}
                text = ' '.join(c.get('text', '') for c in (r.get('message') or {}).get('content') or [] if isinstance(c, dict))
                text = re.sub(r'\([^)]*\)', '(<zone>)', re.sub(r'\d', '9', text))
                shapes[(r.get('version'), 'agent' if r.get('isSidechain') else 'lead', q.get('rateLimitType'),
                        'resetsAt' in q, q.get('status'), r.get('apiError'), info.get('rateLimitType'), text)] += 1
print('count  version  loop  quotaLimits.rateLimitType  resetsAt  status  apiError  rate_limit_info.rateLimitType  message')
for k, n in sorted(shapes.items(), key=lambda kv: (kv[0][0] or '', -kv[1])):
    print(n, *k)
