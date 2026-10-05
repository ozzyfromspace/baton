"""S3b: S3 with a /clear before the late "undo": the model remembers nothing, so only the injected record
can tell it what was done without the human.

S3: the timed ask, end to end, with a stand-in for baton's hooks.

1. The model (told what `baton ask` would print) puts a 3-option question to the human.
2. Nobody answers; the "host" types "3" (Go ahead) after a short timer.
3. PostToolUse(AskUserQuestion) answers with additionalContext: proceed with the fallback.
   Does the model carry out the fallback?
4. Later the human types "undo what you did while I was away". UserPromptSubmit injects the record of
   decisions made without them (with the undo command). Does the model undo it?
"""
import json, os, shutil, subprocess, sys, time
here = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(here, '..', 'common'))
from scenario import Driver

work = os.path.join(here, 'work-s3clear')
shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
subprocess.run(['git', 'init', '-q', work])
log = os.path.join(work, 'hooks.log')
hook = os.path.join(work, 'fakebaton.py')
QUESTION = 'baton: P4 cannot be committed signed (gpg timed out). If nobody answers by 12:40, I will commit it unsigned.'
open(hook, 'w').write(r'''
import json, sys, time
ev = sys.argv[1]; log = sys.argv[2]
d = json.load(sys.stdin)
open(log, 'a').write(json.dumps({'t': time.strftime('%H:%M:%S'), 'ev': ev, 'tool': d.get('tool_name'),
    'prompt': (d.get('prompt') or '')[:80], 'answers': (d.get('tool_response') or {}).get('answers') if isinstance(d.get('tool_response'), dict) else None,
    'cmd': (d.get('tool_input') or {}).get('command') if isinstance(d.get('tool_input'), dict) else None}) + '\n')
out = None
if ev == 'PostToolUse' and d.get('tool_name') == 'AskUserQuestion':
    ans = list(((d.get('tool_response') or {}).get('answers') or {}).values())
    if ans and ans[0].startswith('Go ahead'):
        out = {'systemMessage': 'baton: no answer in 5m -> proceeding: commit unsigned',
               'hookSpecificOutput': {'hookEventName': 'PostToolUse', 'additionalContext':
               '[baton] Nobody answered within 5 minutes, so baton chose "Go ahead" for the human. Proceed now with your fallback: '
               'create the file fallback.txt containing the word unsigned (this stands in for the unsigned commit). '
               'baton recorded the decision; if the human later asks for it to be undone, the undo is: rm fallback.txt'}}
elif ev == 'UserPromptSubmit' and not (d.get('prompt') or '').lstrip().startswith(('[baton]', '<task-notification>')):
    out = {'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit', 'additionalContext':
           '[baton] Decisions this run made without the human since they last took part: '
           '1) 12:40 P4 — nobody answered "commit unsigned?", so the fallback ran: created fallback.txt (stands in for an unsigned commit). '
           'Undo: rm fallback.txt'}}
if out:
    print(json.dumps(out))
''')
settings = {
    'hooks': {ev: [{'hooks': [{'type': 'command', 'command': f'python3 "{hook}" {ev} "{log}"'}]}]
              for ev in ['UserPromptSubmit', 'PermissionRequest', 'PostToolUse', 'Stop']},
    'permissions': {'allow': ['Write', 'Bash(rm fallback.txt)', 'Bash(rm:*)', 'Bash(ls:*)', 'Bash(cat:*)']},
}
prompt = ('You are running a plan under baton, which just told you this after you ran `baton ask`: '
          '"baton: recorded. Now call AskUserQuestion with exactly one question: \"' + QUESTION + '\" (header \"P4 commit\") and exactly three options, in this order: '
          '\"Wait for me\" (description \"Stop the clock; I will answer in the session\"), '
          '\"Pause baton\" (description \"I am taking over\"), '
          '\"Go ahead\" (description \"Commit unsigned now; re-sign later with git commit --amend --no-edit -S\"). '
          'baton answers it itself if nobody has by 12:40; then follow what it tells you." Do that now.')


def events():
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []


d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps(settings), prompt])
d.trust()
d.wait(lambda: any(e['ev'] == 'PermissionRequest' and e['tool'] == 'AskUserQuestion' for e in events()), 90, 'question open')
time.sleep(8)  # the timer
d.key(b'3'); d.note('host typed 3 (Go ahead)')
d.wait(lambda: os.path.exists(os.path.join(work, 'fallback.txt')), 60, 'fallback carried out')
d.wait(lambda: any(e['ev'] == 'Stop' for e in events()), 60, 'turn end')
fallback_done = os.path.exists(os.path.join(work, 'fallback.txt'))
time.sleep(2); d.wait_quiet(1.5, 10)
d.type('/clear'); d.enter(); time.sleep(4); d.wait_quiet(1.5, 15)
d.type('I am back. Undo whatever you did while I was away.'); d.enter()
d.wait(lambda: not os.path.exists(os.path.join(work, 'fallback.txt')), 90, 'undo')
d.wait(lambda: sum(1 for e in events() if e['ev'] == 'Stop') >= 2, 60, 'second turn end')
time.sleep(1)
d.type('/exit'); d.enter(); time.sleep(3); d.close()
print('fallback carried out:', fallback_done, '| undone:', not os.path.exists(os.path.join(work, 'fallback.txt')))
for e in events():
    print(e)
