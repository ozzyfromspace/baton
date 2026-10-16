#!/usr/bin/env python3
"""Spike 39: what Escape does to a question of baton's and to a permission prompt, and what each digit means in
today's permission prompt (Haiku).

Usage: run.py <scenario> <workroot>. claude runs in a PTY in a fresh git repo (raw bytes kept, so pyte renders the
screen as drawn), with a settings hook on every event (hook.py, logging to hooks.jsonl). No mod: this is serial mode,
where the host would type. SPK_CLAUDE names the claude binary.

  Q   default mode: baton's question about a plan approved mid-run (new order: Wait for me, Start the new plan from
      P0, Keep the current run) dismissed with Esc; then asked again and answered by a stray Enter
  T   default mode: Esc on the question with its free-text option focused and words typed there; the idle
      notification after a dismissal; what the digit 4 (Type something) does there
  P   default mode: the lead's Bash permission prompt (its options; Esc), a Write prompt (its options; Esc), and a
      Bash prompt answered 3
  A   acceptEdits mode: the lead's Bash permission prompt (its options; Esc), and one answered 3
  G   default mode, background agents: Esc at the idle prompt with an agent at work, Esc on a draft, Esc during a
      lead turn with an agent at work, and Esc on an agent's permission prompt
"""
import json, os, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver, ANSI
import pyte  # the spike runs under a venv that has it

HOOK = os.path.join(HERE, 'hook.py')
CLAUDE = os.environ.get('SPK_CLAUDE') or 'claude'
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'PermissionDenied', 'Notification', 'Stop', 'StopFailure', 'SubagentStart',
          'SubagentStop']
ROWS, COLS = 50, 160
ESC, CTRL_U = b'\x1b', b'\x15'

QUESTION = "baton: you approved a new plan while 'Demo' is running (at P1). Start the new plan from P0, or keep the current run?"
OPTIONS = [('Wait for me', "Change nothing yet; I'll write in the session, or the current run goes on after 20 minutes with no word"),
           ('Start the new plan from P0', 'Stop the current run and run the plan you just approved'),
           ('Keep the current run', 'Leave the approved plan aside; the current run goes on')]
ASK = ('Call the AskUserQuestion tool with exactly one question, %s (header "baton"), and exactly 3 options, in this order: %s; '
       'not multi-select. Once it returns, reply with one line: RESULT: followed by what the tool result said, word for word.') % (
    json.dumps(QUESTION), ', '.join('%s (description: %s)' % (json.dumps(l), json.dumps(d)) for l, d in OPTIONS))


class RawDriver(Driver):
    """The common driver, keeping the raw bytes too (screen.raw), so pyte renders the screen as drawn."""

    def _pump(self):
        raw = open(os.path.join(self.cwd, 'screen.raw'), 'wb')
        while True:
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                break
            if not data:
                break
            clean = ANSI.sub(b'', data)
            with self.lock:
                raw.write(data); raw.flush()
                self.screen.write(clean); self.screen.flush()
                self.tail = (self.tail + clean)[-20000:]
                self.last_out = time.time()
            self.answer(data)
        self.alive = False

    def answer(self, data):
        """A terminal's answers to claude's queries: none (a terminal that knows none of them)."""


class KittyDriver(RawDriver):
    """A terminal that speaks the kitty keyboard protocol, as several do: it answers claude's query for the
    protocol's flags (CSI ? u) with the flags claude pushed (CSI > 5 u), and its device queries, so claude takes
    keys to come encoded (Esc as CSI 27 u), while the driver still types a bare Esc byte, as baton's host would."""

    def answer(self, data):
        try:
            if b'\x1b[?u' in data:
                os.write(self.fd, b'\x1b[?5u')
            if b'\x1b[>0q' in data:
                os.write(self.fd, b'\x1bP>|kitty(0.39.1)\x1b\\')
            if b'\x1b[c' in data:
                os.write(self.fd, b'\x1b[?62;22c')
        except OSError:
            pass


def git(cwd, *a):
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def setup(work):
    shutil.rmtree(work, ignore_errors=True)
    repo = os.path.join(work, 'repo')
    os.makedirs(os.path.join(repo, '.claude'))
    git(repo, 'init', '-q', '-b', 'main')
    files = {'README': 'spike\n', 'ask.sh': 'echo asked >> ran.txt\necho ask.sh ran\n', 'ask2.sh': 'echo asked2 >> ran.txt\necho ask2.sh ran\n',
             'slow.sh': 'sleep 25\necho slept\n', 'long.sh': 'sleep 60\necho slept long\n', '.gitignore': 'screen.*\nran.txt\n',
             # a spike never touches the human's plans folder
             '.claude/settings.json': json.dumps({'plansDirectory': os.path.join(work, 'plans')})}
    for name, text in files.items():
        open(os.path.join(repo, name), 'w').write(text)
    git(repo, 'add', '.')
    git(repo, 'commit', '-qm', 'init')
    return repo


def settings(work):
    log = os.path.join(work, 'hooks.jsonl')
    return json.dumps({'hooks': {ev: [{'hooks': [{'type': 'command', 'command': f'python3 "{HOOK}" {ev} "{log}"'}]}] for ev in EVENTS}})


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


class Run:
    def __init__(self, root, name, mode='default', allow=(), driver=RawDriver):
        self.work = os.path.join(root, name)
        self.repo = setup(self.work)
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        argv = [CLAUDE, '--model', 'haiku', '--permission-mode', mode, '--settings', settings(self.work)]
        for a in ('Bash(pwd)',) + tuple(allow):
            argv += ['--allowedTools', a]
        env = {'BATON_HOME': os.path.join(self.work, 'baton-home')}
        self.d = driver(self.repo, argv, env_extra=env, rows=ROWS, cols=COLS)
        self.version = subprocess.run([CLAUDE, '--version'], capture_output=True, text=True).stdout.strip()
        self.found = {'claude': self.version, 'mode': mode}

    def screen(self):
        with self.d.lock:
            raw = open(os.path.join(self.repo, 'screen.raw'), 'rb').read()
        scr = pyte.Screen(COLS, ROWS)
        pyte.ByteStream(scr).feed(raw)
        return '\n'.join(line.rstrip() for line in scr.display)

    def shows(self, needle):
        return needle in self.screen()

    def dump(self, label, settle=1.0):
        time.sleep(settle)
        txt = self.screen()
        with open(os.path.join(self.work, f'screen-{label}.txt'), 'w') as f:
            f.write(txt)
        self.d.note(f'screen: {label}')
        return txt

    def mark(self, label):
        with open(self.hooks, 'a') as f:
            f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'input': {'step': label}}) + '\n')
        self.d.note(label)
        return len(recs(self.hooks))

    def ev(self, name, pred=lambda r: True, since=0):
        return [r for r in recs(self.hooks)[since:] if r['ev'] == name and pred(r)]

    def since(self, n):
        """The hooks that fired after the n-th log line: event, agent, tool, and what a tool's end or a stop says."""
        out = []
        for r in recs(self.hooks)[n:]:
            i = r.get('input', {})
            if r['ev'] == 'DRIVER':
                continue
            row = {'hms': r['hms'], 'ev': r['ev']}
            for k in ('agent_id', 'tool_name', 'notification_type', 'message', 'reason', 'error', 'is_interrupt', 'permission_mode', 'stop_hook_active', 'last_assistant_message'):
                if i.get(k) not in (None, ''):
                    row[k] = i[k] if k != 'agent_id' else i[k][:8]
            if r['ev'] in ('PostToolUse', 'PostToolUseFailure', 'PermissionDenied'):
                row['response'] = i.get('tool_response')
            if r['ev'] in ('PreToolUse', 'PermissionRequest') and i.get('tool_name') == 'Bash':
                row['command'] = (i.get('tool_input') or {}).get('command')
            if r['ev'] == 'UserPromptSubmit':
                row['prompt'] = (i.get('prompt') or '')[:200]
            out.append(row)
        return out

    def cmd(self, text):
        self.mark('type: ' + text[:120])
        self.d.type(text, delay=0.01)
        time.sleep(0.5)
        self.d.enter()
        time.sleep(1.5)

    def key(self, raw, label, settle=0.6):
        n = self.mark('key: ' + label)
        self.d.key(raw)
        time.sleep(settle)
        return n

    def stops(self):
        return len(self.ev('Stop', lambda x: not x['input'].get('agent_id'))) + len(self.ev('StopFailure', lambda x: not x['input'].get('agent_id')))

    def ask(self, text, timeout=120):
        n = self.stops()
        self.cmd(text)
        ok = self.d.wait(lambda: self.stops() > n, timeout, 'lead stop')
        self.d.wait_quiet(2, 20)
        return ok

    def wait(self, cond, timeout, what):
        return self.d.wait(cond, timeout, what)

    def start(self):
        self.d.trust(25)
        self.d.wait_quiet(3, 40)
        self.ask('Reply with only the word ready.')

    def asked(self, tool, command=None, agent=False, since=0):
        return self.ev('PermissionRequest', lambda x: x['input'].get('tool_name') == tool and bool(x['input'].get('agent_id')) == agent and
                       (command is None or (x['input'].get('tool_input') or {}).get('command') == command), since)

    def ran(self):
        try:
            return open(os.path.join(self.repo, 'ran.txt')).read().split()
        except FileNotFoundError:
            return []

    def probe(self, label, key, settle=12):
        """Press key with a dialog on screen: the screen before and after, the hooks that fired, and what ran."""
        before = self.dump(f'{label}-before', 2)
        ran = self.ran()
        n = self.key(key, f'{label}: {key!r}', settle)
        after = self.dump(f'{label}-after', 0.5)
        self.found[label] = {'before': dialog(before), 'after': dialog(after), 'hooks': self.since(n), 'ran_before': ran, 'ran_after': self.ran()}
        return self.found[label]

    def finish(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()

    def summary(self):
        tps = sorted({r['input'].get('transcript_path') for r in recs(self.hooks) if r['input'].get('transcript_path')})
        self.found['results'] = []
        for tp in tps:
            if not os.path.exists(tp):
                continue
            shutil.copy(tp, os.path.join(self.work, 'transcript-' + os.path.basename(tp)))
            self.found['results'] += results(tp)
            sub = os.path.join(tp[:-len('.jsonl')], 'subagents')
            if os.path.isdir(sub):
                shutil.copytree(sub, os.path.join(self.work, 'subagents'), dirs_exist_ok=True)
                for p in sorted(os.listdir(sub)):
                    if p.endswith('.jsonl'):
                        self.found['results'] += [dict(r, agent=p[:-6]) for r in results(os.path.join(sub, p))]
        with open(os.path.join(self.work, 'summary.json'), 'w') as f:
            json.dump(self.found, f, indent=1)
        print(json.dumps(self.found, indent=1))


def dialog(screen):
    """The screen's lines from the last that names a dialog (a question's text, a permission prompt's header) down."""
    lines = screen.splitlines()
    for i in range(len(lines) - 1, -1, -1):
        if any(k in lines[i] for k in ('Bash command', 'Do you want', 'baton', 'Create file', 'Write', 'Interrupted', 'What should Claude')):
            start = max(0, i - 3)
            return '\n'.join(l for l in lines[start:] if l.strip())[-2500:]
    return '\n'.join(l for l in lines if l.strip())[-1200:]


def results(tp):
    """Each tool call of a transcript with its result, and every interruption note."""
    calls, out = {}, []
    for line in open(tp):
        try:
            e = json.loads(line)
        except Exception:
            continue
        msg = e.get('message') or {}
        content = msg.get('content')
        if not isinstance(content, list):
            if isinstance(content, str) and 'interrupt' in content.lower():
                out.append({'note': content[:300]})
            continue
        for c in content:
            if c.get('type') == 'tool_use':
                inp = c.get('input') or {}
                calls[c.get('id')] = {'tool': c.get('name'), 'command': inp.get('command'), 'questions': [q.get('question') for q in inp.get('questions') or []] or None}
            elif c.get('type') == 'tool_result':
                body = c.get('content')
                if isinstance(body, list):
                    body = ' '.join(x.get('text', '') for x in body if isinstance(x, dict))
                out.append({**calls.get(c.get('tool_use_id'), {}), 'is_error': c.get('is_error'), 'result': (body or '')[:600],
                            'toolUseResult': str(e.get('toolUseResult'))[:300]})
            elif c.get('type') == 'text' and 'interrupt' in (c.get('text') or '').lower():
                out.append({'note': c['text'][:300]})
    return out


def Q(root):
    r = Run(root, 'Q')
    r.start()
    # (q1) baton's question, dismissed with Esc
    n = r.stops()
    r.cmd(ASK)
    r.wait(lambda: r.asked('AskUserQuestion') and r.shows('Keep the current run'), 120, 'the question on screen')
    q1 = r.probe('q1-esc', ESC, 12)
    r.wait(lambda: r.stops() > n, 30, 'a stop after Esc (if one comes)')
    q1['stopped'] = r.stops() > n
    r.dump('q1-later', 3)
    # what the model saw, in its own words, at its next turn
    r.ask('What did the AskUserQuestion tool result say, word for word? Reply with one line: SAW: followed by it.')
    r.dump('q1-saw', 1)
    # (q2) asked again, answered by a stray Enter
    n = r.stops()
    m = len(r.asked('AskUserQuestion'))
    r.cmd(ASK)
    r.wait(lambda: len(r.asked('AskUserQuestion')) > m and r.shows('Keep the current run'), 120, 'the question on screen again')
    r.probe('q2-enter', b'\r', 8)
    r.wait(lambda: r.stops() > n, 60, 'the stop after Enter')
    r.dump('q2-later', 2)
    r.finish()
    r.summary()


DOWN = b'\x1b[B'


def T(root):
    """Esc on baton's question with its free-text option focused and a few words typed there (a human who began an
    answer and walked away), and what follows a dismissal: the idle notification that corrects baton's record."""
    r = Run(root, 'T')
    r.start()
    # (t1) the free-text option, words typed, then Esc
    r.cmd(ASK)
    r.wait(lambda: r.asked('AskUserQuestion') and r.shows('Keep the current run'), 120, 'the question on screen')
    time.sleep(2)
    for i in range(3):
        r.key(DOWN, 'down', 0.5)
    r.mark('type into Type something')
    r.d.type('partial answer', delay=0.03)
    time.sleep(1)
    t1 = r.probe('t1-esc-text', ESC, 6)
    if 'Keep the current run' in r.screen():
        t1['second'] = r.probe('t1-esc-text-again', ESC, 6)
    # the idle notification after the dismissal (about a minute after the turn's end)
    m = r.mark('wait for idle_prompt')
    r.wait(lambda: r.ev('Notification', since=m), 100, 'a notification after the dismissal')
    t1['notifications'] = [{'hms': x['hms'], 'type': x['input'].get('notification_type'), 'message': x['input'].get('message')} for x in r.ev('Notification', since=m)]
    r.dump('t1-later', 1)
    # (t2) a digit typed into the question: what 4 (Type something) does
    m = len(r.asked('AskUserQuestion'))
    r.cmd(ASK)
    r.wait(lambda: len(r.asked('AskUserQuestion')) > m and r.shows('Keep the current run'), 120, 'the question on screen again')
    r.probe('t2-four', b'4', 3)
    r.probe('t2-four-esc', ESC, 6)
    r.d.wait_quiet(3, 30)
    r.finish()
    r.summary()


def I(root):
    """After one Esc on baton's question, the session left alone for 3 minutes: does Claude Code's idle notification
    come, which corrects a turn baton still thinks open (the Stop hook does not run for an interrupted turn)? Then,
    for comparison, a turn that ends normally, left alone the same."""
    r = Run(root, 'I')
    r.start()
    r.cmd(ASK)
    r.wait(lambda: r.asked('AskUserQuestion') and r.shows('Esc to cancel'), 120, 'the question on screen')
    time.sleep(2)
    m = r.key(ESC, 'esc on the question', 1)
    r.wait(lambda: r.ev('Notification', since=m), 180, 'a notification after the dismissal')
    r.found['after_dismissal'] = [{'hms': x['hms'], 'type': x['input'].get('notification_type')} for x in r.ev('Notification', since=m)]
    m = r.mark('a turn that ends normally')
    r.ask('Reply with only the word again.')
    r.wait(lambda: r.ev('Notification', since=m), 180, 'a notification after a normal turn')
    r.found['after_turn'] = [{'hms': x['hms'], 'type': x['input'].get('notification_type')} for x in r.ev('Notification', since=m)]
    r.finish()
    r.summary()


def K(root):
    """Under a terminal that speaks the kitty keyboard protocol: does a bare Esc byte still dismiss baton's question
    and reject a permission prompt? If not, does Esc encoded (CSI 27 u)?"""
    r = Run(root, 'K', driver=KittyDriver)
    r.start()
    n = r.stops()
    r.cmd(ASK)
    r.wait(lambda: r.asked('AskUserQuestion') and r.shows('Esc to cancel'), 120, 'the question on screen')
    k1 = r.probe('k1-esc', ESC, 8)
    k1['dismissed'] = 'declined to answer' in r.screen()
    if not k1['dismissed']:
        k1['encoded'] = r.probe('k1-esc-encoded', b'\x1b[27u', 8)
        k1['encoded']['dismissed'] = 'declined to answer' in r.screen()
    r.d.wait_quiet(2, 20)
    r.cmd('Run exactly this one Bash command and nothing else: sh ask.sh — then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Bash', 'sh ask.sh') and r.shows('Bash command'), 120, 'the Bash prompt on screen')
    k2 = r.probe('k2-esc', ESC, 8)
    k2['rejected'] = 'Interrupted' in r.screen()
    if not k2['rejected']:
        k2['encoded'] = r.probe('k2-esc-encoded', b'\x1b[27u', 8)
        k2['encoded']['rejected'] = 'Interrupted' in r.screen()
    r.d.wait_quiet(2, 20)
    r.finish()
    r.summary()


def P(root):
    r = Run(root, 'P')
    r.start()
    # (p1) the lead's Bash prompt, Esc
    n = r.stops()
    r.cmd('Run exactly this one Bash command and nothing else: sh ask.sh — then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Bash', 'sh ask.sh') and r.shows('Bash command'), 120, 'the Bash prompt on screen')
    p1 = r.probe('p1-esc', ESC, 12)
    r.wait(lambda: r.stops() > n, 20, 'a stop after Esc (if one comes)')
    p1['stopped'] = r.stops() > n
    r.dump('p1-later', 2)
    # (p2) a Write prompt, Esc
    n = r.stops()
    r.cmd('Use the Write tool to create the file notes.txt containing exactly the word ok. Then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Write') and (r.shows('Create file') or r.shows('Do you want')), 120, 'the Write prompt on screen')
    p2 = r.probe('p2-esc', ESC, 12)
    r.wait(lambda: r.stops() > n, 20, 'a stop after Esc (if one comes)')
    p2['stopped'] = r.stops() > n
    p2['notes_exists'] = os.path.exists(os.path.join(r.repo, 'notes.txt'))
    r.dump('p2-later', 2)
    # (p3) the lead's Bash prompt, 3
    n = r.stops()
    r.cmd('Run exactly this one Bash command and nothing else: sh ask2.sh — then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Bash', 'sh ask2.sh') and r.shows('Bash command'), 120, 'the second Bash prompt on screen')
    p3 = r.probe('p3-three', b'3', 12)
    r.wait(lambda: r.stops() > n, 30, 'a stop after 3 (if one comes)')
    p3['stopped'] = r.stops() > n
    r.dump('p3-later', 2)
    # the permission mode now, as the next prompt's hook reports it
    m = r.mark('mode probe')
    r.ask('Run exactly this one Bash command and nothing else: pwd — then reply with only the word DONE.')
    p3['mode_after'] = [x['input'].get('permission_mode') for x in r.ev('UserPromptSubmit', since=m)]
    r.dump('p3-mode', 1)
    r.finish()
    r.summary()


def A(root):
    r = Run(root, 'A', mode='acceptEdits')
    r.start()
    n = r.stops()
    r.cmd('Run exactly this one Bash command and nothing else: sh ask.sh — then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Bash', 'sh ask.sh') and r.shows('Bash command'), 120, 'the Bash prompt on screen')
    a1 = r.probe('a1-esc', ESC, 12)
    r.wait(lambda: r.stops() > n, 20, 'a stop after Esc (if one comes)')
    a1['stopped'] = r.stops() > n
    n = r.stops()
    r.cmd('Run exactly this one Bash command and nothing else: sh ask2.sh — then reply with one line: RESULT: and what the tool result said.')
    r.wait(lambda: r.asked('Bash', 'sh ask2.sh') and r.shows('Bash command'), 120, 'the second Bash prompt on screen')
    a2 = r.probe('a2-three', b'3', 12)
    r.wait(lambda: r.stops() > n, 30, 'a stop after 3 (if one comes)')
    a2['stopped'] = r.stops() > n
    m = r.mark('mode probe')
    r.ask('Run exactly this one Bash command and nothing else: pwd — then reply with only the word DONE.')
    a2['mode_after'] = [x['input'].get('permission_mode') for x in r.ev('UserPromptSubmit', since=m)]
    r.finish()
    r.summary()


AGENT = ('Use the Agent tool with run_in_background set to true, description "%s", and this prompt: "Run the Bash command sh long.sh, '
         'then run the Bash command sh long.sh again, then reply with only the word FINISHED." Do not wait for it: once it is '
         'launched, reply with only the word LAUNCHED.')


def G(root):
    r = Run(root, 'G', allow=('Bash(sh long.sh)', 'Bash(sh slow.sh)'))
    r.start()

    def agent_runs(n):
        return len(r.ev('PreToolUse', lambda x: x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == 'sh long.sh'))

    def agent_done(n):
        return len(r.ev('PostToolUse', lambda x: x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == 'sh long.sh'))

    # (g1) Esc at the idle prompt, the box empty, an agent at work
    r.ask(AGENT % 'G1 agent')
    r.wait(lambda: agent_runs(0) >= 1, 90, 'G1 runs long.sh')
    time.sleep(3)
    r.d.wait_quiet(2, 20)
    g1 = r.probe('g1-esc-idle', ESC, 5)
    # (g2) Esc on a draft in the box, the agent still at work
    r.mark('type a draft')
    r.d.type('a draft the human is writing', delay=0.01)
    time.sleep(1)
    g2 = r.probe('g2-esc-draft', ESC, 3)
    g2['second'] = r.probe('g2-esc-draft-again', ESC, 3)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    # (g3) Esc during a lead turn, the agent still at work
    n = r.stops()
    r.cmd('Run exactly this one Bash command and nothing else: sh slow.sh — then reply with only the word SLEPT.')
    r.wait(lambda: r.ev('PreToolUse', lambda x: not x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == 'sh slow.sh'), 60, 'the lead runs slow.sh')
    time.sleep(3)
    g3 = r.probe('g3-esc-turn', ESC, 5)
    g3['stopped'] = r.stops() > n
    # did the agent live on? its first long.sh ending, its second starting, and its stop
    r.wait(lambda: agent_done(0) >= 1, 90, 'G1 first long.sh done')
    r.wait(lambda: agent_runs(0) >= 2, 30, 'G1 second long.sh')
    r.found['g1-agent'] = {'long_runs': agent_runs(0), 'long_done': agent_done(0)}
    r.wait(lambda: r.ev('SubagentStop'), 90, 'G1 stop')
    r.found['g1-agent']['stopped'] = [x['input'].get('last_assistant_message') for x in r.ev('SubagentStop')]
    r.d.wait_quiet(4, 60)
    # (g4) an agent's permission prompt, Esc
    r.ask('Use the Agent tool with run_in_background set to true, description "G4 agent", and this prompt: "Run the Bash command sh ask.sh, '
          'then reply with one line: RESULT: and what its tool result said." Do not wait for it: once it is launched, reply with only the word LAUNCHED.')
    r.wait(lambda: r.asked('Bash', 'sh ask.sh', agent=True) and r.shows('Bash command'), 120, "G4's prompt on screen")
    r.probe('g4-esc-agent-prompt', ESC, 15)
    r.wait(lambda: len(r.ev('SubagentStop')) >= 2, 60, 'G4 stop')
    r.d.wait_quiet(4, 60)
    r.dump('g4-later', 1)
    r.finish()
    r.summary()


if __name__ == '__main__':
    scen, root = sys.argv[1], os.path.abspath(sys.argv[2])
    os.makedirs(root, exist_ok=True)
    {'Q': Q, 'T': T, 'I': I, 'K': K, 'P': P, 'A': A, 'G': G}[scen](root)
