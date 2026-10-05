"""S1: can baton be certain the dialog it auto-answers is its own?

A: what the digit "3" does in a Bash permission prompt (hoped: No / deny), and "1" (Yes).
B: two dialogs at once (parallel Bash + AskUserQuestion in one message): hook order, which dialog is
   in front, and what "3" does to each.
C: a background subagent's permission prompt while the main agent's AskUserQuestion is open.

usage: python3 s1_dialogs.py A|B|C
"""
import json, os, shutil, subprocess, sys, time
here = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(here, '..', 'common'))
from scenario import Driver

part = sys.argv[1]
work = os.path.join(here, 'work-s1' + part)
shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
subprocess.run(['git', 'init', '-q', work]); open(os.path.join(work, 'README.txt'), 'w').write('probe readme\n' * 50)
log = os.path.join(work, 'hooks.log')
hooklog = os.path.join(here, 'common', 'hooklog.py')
EVENTS = ['PreToolUse', 'PermissionRequest', 'PostToolUse', 'PostToolUseFailure', 'PermissionDenied',
          'Notification', 'Stop', 'SubagentStart', 'SubagentStop', 'UserPromptSubmit']
settings = {'hooks': {ev: [{'hooks': [{'type': 'command', 'command': f'python3 "{hooklog}" {ev} --log "{log}"'}]}] for ev in EVENTS}}


def events():
    try:
        return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError:
        return []


def count(ev, tool=None):
    return sum(1 for e in events() if e['ev'] == ev and (tool is None or e['input'].get('tool_name') == tool))


def snap(d, label):
    time.sleep(1.5); d.wait_quiet(1.0, 10)
    with d.lock:
        tail = d.tail[-2500:].decode('utf-8', 'replace')
    open(os.path.join(work, f'snap-{label}.txt'), 'w').write(tail)
    d.note(f'snapshot {label}')


prompts = {
    'A': 'Run exactly this one Bash command and nothing else: touch probe_a.txt — then reply with one word: ok.',
    'B': ('In a SINGLE assistant message make exactly two tool calls in parallel: (1) Bash with command "touch probe_b.txt", '
          'and (2) AskUserQuestion with one question "probe: parallel?" (header "Probe") and exactly three options '
          '"Alpha" (description "first"), "Beta" (description "second"), "Gamma" (description "third"). '
          'After both results, reply with the chosen label and whether the file exists.'),
    'D': ('Do these three things in order. First, use the Agent tool with run_in_background set to true and the prompt: '
          '"Run the Bash command: touch probe_d.txt — then report done." Second, use the Read tool to read README.txt in the current directory. '
          'Third, call AskUserQuestion with one question "probe: queued?" (header "Probe") and exactly three options '
          '"Alpha" (description "first"), "Beta" (description "second"), "Gamma" (description "third"). Then reply with the chosen label.'),
    'C': ('Do these two things, in this order, without waiting in between. First, use the Agent tool with run_in_background set to true '
          'and the prompt: "Run the Bash command: sleep 3 && touch probe_c.txt — then report done." '
          'Second, immediately after launching it, call AskUserQuestion with one question "probe: background?" (header "Probe") '
          'and exactly three options "Alpha" (description "first"), "Beta" (description "second"), "Gamma" (description "third"). '
          'Then reply with the chosen label.'),
}

d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps(settings), prompts[part]])
d.trust()

if part == 'A':
    d.wait(lambda: count('PermissionRequest', 'Bash') >= 1, 90, 'bash permission prompt')
    snap(d, 'A1-prompt')
    d.key(b'3'); d.note('typed 3')
    time.sleep(4); snap(d, 'A2-after-3')
    # If "3" opened a feedback box, Enter submits it empty.
    d.wait(lambda: count('Stop') >= 1 or count('PostToolUse', 'Bash') >= 1, 20, 'turn end after 3')
    snap(d, 'A3-settled')
    d.type('Now run exactly this one Bash command: touch probe_a2.txt — then reply ok.'); d.enter()
    d.wait(lambda: count('PermissionRequest', 'Bash') >= 2, 60, 'second bash prompt')
    snap(d, 'A4-prompt2')
    d.key(b'1'); d.note('typed 1')
    d.wait(lambda: count('Stop') >= 2, 60, 'stop after 1')
    snap(d, 'A5-done')
elif part == 'B':
    d.wait(lambda: count('PermissionRequest') >= 1, 90, 'first dialog')
    time.sleep(3)
    snap(d, 'B1-first')
    d.key(b'3'); d.note('typed 3 (first)')
    time.sleep(5); snap(d, 'B2-after-first-3')
    if count('PermissionRequest') >= 2 or count('PostToolUse', 'AskUserQuestion') == 0:
        d.key(b'3'); d.note('typed 3 (second)')
    d.wait(lambda: count('Stop') >= 1, 60, 'stop')
    snap(d, 'B3-done')
elif part == 'D':
    d.wait(lambda: count('PermissionRequest', 'AskUserQuestion') >= 1, 120, 'ask requested')
    time.sleep(4)
    snap(d, 'D1-front')
    d.key(b'3'); d.note('typed 3 (first)')
    time.sleep(6); snap(d, 'D2-after-first-3')
    d.key(b'3'); d.note('typed 3 (second)')
    time.sleep(6); snap(d, 'D3-after-second-3')
    d.wait(lambda: count('Stop') >= 1, 60, 'stop')
else:
    d.wait(lambda: count('PermissionRequest', 'AskUserQuestion') >= 1, 120, 'ask dialog')
    # Give the background subagent time to reach its Bash permission prompt while the question is open.
    time.sleep(20)
    snap(d, 'C1-question-open')
    d.key(b'3'); d.note('typed 3 (first)')
    time.sleep(6); snap(d, 'C2-after-first-3')
    d.wait(lambda: count('Stop') >= 1, 60, 'stop')
    time.sleep(15); snap(d, 'C3-later')
    if any(e['ev'] == 'PermissionRequest' and e['input'].get('tool_name') == 'Bash' for e in events()) and count('PostToolUse', 'Bash') == 0:
        d.key(b'3'); d.note('typed 3 (bash still open?)'); time.sleep(5); snap(d, 'C4-after-second-3')

d.type('/exit'); d.enter(); time.sleep(3); d.close()
print('files:', sorted(f for f in os.listdir(work) if f.startswith('probe')))
for e in events():
    i = e['input']
    extra = {k: i.get(k) for k in ('tool_name', 'agent_id', 'agent_type', 'tool_use_id', 'notification_type', 'reason') if i.get(k)}
    if e['ev'] == 'PostToolUse' and i.get('tool_name') == 'AskUserQuestion':
        extra['answers'] = (i.get('tool_response') or {}).get('answers')
    if e['ev'] in ('PostToolUseFailure', 'PermissionDenied'):
        extra['err'] = str(i.get('error') or i.get('tool_response') or i.get('reason'))[:160]
    if e['ev'] == 'PermissionRequest':
        extra['keys'] = sorted(i.keys())
    print(e['hms'], e['ev'], json.dumps(extra))
