#!/usr/bin/env python3
"""Spike 38 hook: append one JSON line per event to the log named on the command line, with the fields the
spike reads (the tool, the plan file an ExitPlanMode names, the compaction's trigger, the transcript)."""
import json, sys, time

ev, log = sys.argv[1], sys.argv[2]
try:
    inp = json.load(sys.stdin)
except Exception:
    inp = {}
resp = inp.get('tool_response') if isinstance(inp.get('tool_response'), dict) else {}
ti = inp.get('tool_input') if isinstance(inp.get('tool_input'), dict) else {}
rec = {
    't': round(time.time(), 3), 'hms': time.strftime('%H:%M:%S'), 'ev': ev,
    'session': inp.get('session_id'), 'transcript': inp.get('transcript_path'),
    'tool': inp.get('tool_name'), 'mode': inp.get('permission_mode'), 'trigger': inp.get('trigger'),
    'source': inp.get('source'), 'file': resp.get('filePath') or ti.get('planFilePath'),
}
with open(log, 'a') as f:
    f.write(json.dumps({k: v for k, v in rec.items() if v is not None}) + '\n')
