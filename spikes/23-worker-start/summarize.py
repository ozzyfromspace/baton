"""Prints one line per run: hook times (s after launch), SessionEnd reason, from out/*-run.json and logs/*.log."""
import json, os, glob
HERE = os.path.dirname(os.path.abspath(__file__))
for run in sorted(glob.glob(os.path.join(HERE, 'out', '*-run.json'))):
    name = os.path.basename(run)[:-len('-run.json')]
    log = os.path.join(HERE, 'logs', name + '.log')
    if not os.path.exists(log): log = os.path.join(HERE, 'logs', name.replace('-swallowed', '') + '-swallowed.log')
    t0 = json.load(open(run))['marks'][0][0]
    recs = [json.loads(l) for l in open(log)] if os.path.exists(log) else []
    first = {}
    for r in recs: first.setdefault(r['ev'], r)
    f = lambda ev: f"{first[ev]['t'] - t0:5.2f}" if ev in first else '   - '
    end = (first.get('SessionEnd') or {}).get('input') or {}
    print(f"{name:20} SessionStart {f('SessionStart')}  UserPromptSubmit {f('UserPromptSubmit')}  Stop {f('Stop')}  SessionEnd {f('SessionEnd')} {end.get('reason', '')}")
