#!/usr/bin/env python3
"""Spike 42: what does the status line input say about the account's usage windows?

    python3 capture.py [model] [idle seconds] [agent]      # defaults: haiku 90

Starts an interactive claude in a pty, in a scratch project with a scratch BATON_HOME, with a status line
command that appends each input it gets, stamped with the time, to statusline.jsonl in a temporary folder
(kept, outside the repo). One short turn, then the session sits idle so the log shows whether the input
refreshes with nothing happening. Prints the rate_limits of each distinct input, and the gaps between inputs
during the turn and while idle. With `agent`, the turn starts a background agent that keeps working after the
turn ends, to show whether the input refreshes while only an agent works."""
import json, os, shutil, sys, tempfile, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

model = sys.argv[1] if len(sys.argv) > 1 else 'haiku'
idle = int(sys.argv[2]) if len(sys.argv) > 2 else 90
agent = len(sys.argv) > 3 and sys.argv[3] == 'agent'
work = tempfile.mkdtemp(prefix='spike42-work-')  # kept: the raw inputs, outside the repo
log = os.path.join(work, 'statusline.jsonl')
project = tempfile.mkdtemp(prefix='spike42-')
home = tempfile.mkdtemp(prefix='spike42-baton-')

# The logger stamps each input with when it arrived; the status line itself shows one word.
logger = os.path.join(work, 'logger.py')
with open(logger, 'w') as f:
    f.write('import json, sys, time\n'
            'd = json.load(sys.stdin)\n'
            'd["_at"] = time.time()\n'
            f'open({log!r}, "a").write(json.dumps(d) + "\\n")\n'
            'print("probe")\n')
settings = {'statusLine': {'type': 'command', 'command': f'python3 "{logger}"'}}
prompt = 'Run `echo hi` with the Bash tool, then reply with the single word ok.'
if agent:
    settings['permissions'] = {'allow': ['Bash(sleep 8)', 'Bash(echo tick)']}
    prompt = ('Start one general-purpose agent with the Agent tool, run_in_background true, telling it: "Run the Bash '
              'command `sleep 8`, then `echo tick`, and repeat that pair six times, one command per tool call, then '
              'reply done." Once it is started, reply with the single word started and end your turn.')
argv = ['claude', '--model', model, '--settings', json.dumps(settings), prompt]
d = Driver(project, argv, env_extra={'BATON_HOME': home})
d.trust()


def lines():
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except Exception:
        return []


def answered():
    return any(r.get('rate_limits') or (r.get('context_window') or {}).get('used_percentage') for r in lines())


d.wait(answered, 180, 'status line with usage')
d.wait_quiet(3, 60)
turn_end = time.time()
d.note(f'turn over; idle for {idle}s')
time.sleep(idle)
d.type('/exit'); d.enter(); time.sleep(3); d.close()

rows = lines()
print(f'{len(rows)} inputs in {log}; claude {rows[-1].get("version") if rows else "?"}')
seen = set()
for r in rows:
    rl = json.dumps(r.get('rate_limits'), sort_keys=True)
    if rl not in seen:
        seen.add(rl)
        print('rate_limits', rl)
print('top-level keys', sorted({k for r in rows for k in r if k != '_at'}))
at = [r['_at'] for r in rows]
during = [round(b - a, 1) for a, b in zip(at, at[1:]) if b <= turn_end]
after = [round(b - a, 1) for a, b in zip(at, at[1:]) if b > turn_end]
print('gaps during the turn (s)', during)
print('gaps while idle (s)', after, f'({len(after)} inputs in {idle}s idle)')
shutil.rmtree(project, ignore_errors=True); shutil.rmtree(home, ignore_errors=True)
