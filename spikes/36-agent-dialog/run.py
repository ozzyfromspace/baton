#!/usr/bin/env python3
"""Spike 36: a phase agent's permission prompt on the lead's screen, and how a stopped agent looks (Haiku).

Usage: run.py <scenario> <workroot>. Spike 35's harness: claude in a PTY in a fresh git repo, the mod (mod/, a copy
per session) and spike 35's settings hook on every event (../35-mod-drives/hook.py, logging to hooks.jsonl, with the
asyncRewake PostCompact hook that wakes the lead with a brief whose codeword is OSPREY); the mod logs to mod.jsonl.*.
SPK_CLAUDE names the claude binary.

  D   default permission mode: a phase agent's permission prompt open on the lead's screen while the mod compacts the
      lead, submits a prompt to it and asks from its band; the lead's own turn under it; the idle notification; keys
      typed as serial mode types them; the lead's own prompt beside it, and both at once
  E   D's (e) to (g) again, each call's end matched by its id (tool.call's event has no input): /compact and Enter,
      then a line and Enter, typed into an agent's prompt; the lead's own prompt; an agent's and the lead's at once
  X   acceptEdits mode: an agent stopped each way the human can stop one (x on its row, running and idle; ctrl+x
      ctrl+k; Ctrl-C at the prompt; Esc in its transcript view), by the mod's TaskStop as baton retires one, and by
      /clear: what the mod, the hooks and the transcripts see, and what a message to it answers
"""
import glob, json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver, ANSI
import pyte  # the spike runs under a venv that has it

HOOK = os.path.join(HERE, '..', '35-mod-drives', 'hook.py')
MOD = os.path.join(HERE, 'mod')
CLAUDE = os.environ.get('SPK_CLAUDE') or 'claude'
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact',
          'SubagentStart', 'SubagentStop', 'WorktreeCreate', 'WorktreeRemove', 'PermissionDenied']
ROWS, COLS = 50, 160
ESC, DOWN, CTRL_C, CTRL_U, CTRL_X, CTRL_K = b'\x1b', b'\x1b[B', b'\x03', b'\x15', b'\x18', b'\x0b'
BACK = 'Reply with only the word BACK.'


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
        self.alive = False


def git(cwd, *a):
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def setup(work):
    shutil.rmtree(work, ignore_errors=True)
    repo = os.path.join(work, 'repo')
    os.makedirs(repo)
    git(repo, 'init', '-q', '-b', 'main')
    files = {'README': 'spike\n', 'slow.sh': 'sleep 25\necho slept\n', 'long.sh': 'sleep 60\necho slept long\n',
             'ask.sh': 'echo asked once\n', 'ask2.sh': 'echo asked twice\n', 'lead.sh': 'echo the lead asked\n',
             '.gitignore': 'screen.*\n'}
    for name, text in files.items():
        open(os.path.join(repo, name), 'w').write(text)
    git(repo, 'add', '.')
    git(repo, 'commit', '-qm', 'init')
    return repo


def settings(work):
    log = shlex.quote(os.path.join(work, 'hooks.jsonl'))
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {log}'}]}] for ev in EVENTS}
    hooks['PostCompact'].append({'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} rewake {log}', 'asyncRewake': True, 'timeout': 600}]})
    return json.dumps({'hooks': hooks})


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


class Run:
    def __init__(self, root, name, env=None, mode='acceptEdits', allow=()):
        self.work = os.path.join(root, name)
        self.repo = setup(self.work)
        mod = self.moddir = os.path.join(self.work, 'mod')
        shutil.copytree(MOD, mod, ignore=shutil.ignore_patterns('types', 'tsconfig.json'))
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.mod = os.path.join(self.work, 'mod.jsonl')
        argv = [CLAUDE, '--model', 'haiku', '--permission-mode', mode, '--plugin-dir', mod, '--settings', settings(self.work)]
        for a in ('Bash(pwd)', 'Bash(sh slow.sh)', 'Bash(sh long.sh)') + tuple(allow):
            argv += ['--allowedTools', a]
        e = {'SPK_LOG': self.mod, 'SPK_SCEN': name + str(int(time.time()))}
        e.update(env or {})
        self.d = RawDriver(self.repo, argv, env_extra=e, rows=ROWS, cols=COLS)
        self.t0 = time.time()
        self.version = subprocess.run([CLAUDE, '--version'], capture_output=True, text=True).stdout.strip()

    def screen(self):
        with self.d.lock:
            raw = open(os.path.join(self.repo, 'screen.raw'), 'rb').read()
        scr = pyte.Screen(COLS, ROWS)
        pyte.ByteStream(scr).feed(raw)
        return '\n'.join(line.rstrip() for line in scr.display)

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

    def ev(self, name, pred=lambda r: True):
        return [r for r in recs(self.hooks) if r['ev'] == name and pred(r)]

    def modrecs(self):
        out = []
        for p in sorted(glob.glob(self.mod + '.*')):
            out += recs(p)
        return sorted(out, key=lambda r: r['t'])

    def modk(self, kind, pred=lambda r: True):
        return [r for r in self.modrecs() if r['kind'] == kind and pred(r)]

    def cmd(self, text):
        self.mark('type: ' + text[:120])
        self.d.type(text)
        time.sleep(0.4)
        self.d.enter()
        time.sleep(1.5)

    def key(self, raw, label, settle=0.6):
        self.mark('key: ' + label)
        self.d.key(raw)
        time.sleep(settle)

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

    def agent(self, name):
        s = self.modk('spawned', lambda x: x['name'] == name)
        return s[-1]['r'].get('agentId') if s else None

    def turns(self, name):
        aid = self.agent(name)
        return [x for x in self.modk('turn.complete') if aid and x.get('agentId') == aid]

    def running(self, name, command):
        aid = self.agent(name)
        return aid and self.ev('PreToolUse', lambda x: x['input'].get('agent_id') == aid and (x['input'].get('tool_input') or {}).get('command') == command)

    def asked(self, name, command):
        """The PermissionRequests of agent name (or the lead, for None) for command."""
        aid = self.agent(name) if name else None
        return self.ev('PermissionRequest', lambda x: (x['input'].get('agent_id') or None) == aid and (x['input'].get('tool_input') or {}).get('command') == command)

    def ended(self, name, command):
        """The mod's call-ends of agent name (or the lead, for None) for command: its dialog answered, the tool run."""
        aid = self.agent(name) if name else None
        # tool.call's event carries no input, so the command comes from the call's tool.check, by its id
        ids = {x.get('id') for x in self.modk('tool.check', lambda x: (x.get('agentId') or None) == aid and x.get('command') == command)}
        return self.modk('call-end', lambda x: x.get('id') in ids)

    def answer(self, name, command, label, tries=3):
        """Answer the dialog of agent name's command with 1 (Yes), once it is the one in front; another dialog of the
        mod's in front (its question) gets 3."""
        for i in range(tries):
            if self.ended(name, command):
                return True
            scr = self.dump(f'{label}-front{i}', 0.5)
            if 'may it commit unsigned' in scr:
                self.key(b'3', f'3 for the spk question in front ({label})', 2)
                continue
            self.key(b'1', f'1 for {name or "the lead"}: {command} ({label})', 3)
            self.wait(lambda: self.ended(name, command), 10, f'{label}: {command} ran')
        return bool(self.ended(name, command))

    def unview(self):
        """Back to the main conversation, if an agent's transcript view is open."""
        for i in range(3):
            v = [x for x in self.modk('render')][-1:]
            scr = self.screen()
            if not ('Message @' in scr or (v and (v[0].get('view') or {}).get('agentId'))):
                return
            self.key(ESC, 'esc: back to the main conversation', 1.5)

    def select(self, name):
        """Focus the agent panel and select name's row: the selected row starts with ❯ (probe)."""
        for i in range(6):
            if any(l.startswith('❯ ') and f' {name} ' in l for l in self.screen().splitlines()):
                return True
            self.key(DOWN, 'down', 0.8)
        self.dump(f'select-{name}', 0.5)
        return False

    def finish(self):
        self.unview()
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()

    def summary(self, extra=None):
        out = {'claude': self.version}
        out['lead_turns'] = [{k: x.get(k) for k in ('reason', 'answer')} for x in self.modk('turn.complete') if not x.get('agentId')]
        out['agent_turns'] = {n: [x.get('reason') for x in self.turns(n)] for n in sorted({x['name'] for x in self.modk('spawned')})}
        out['checks'] = [{k: x.get(k) for k in ('name', 'agentId', 'tool', 'command', 'decision', 'reason', 'decider')} for x in self.modk('tool.check') if x.get('decision') != 'allow']
        out['acts'] = [{k: v for k, v in x.items() if k not in ('t', 'inst')} for x in self.modrecs() if x['kind'] in (
            'act-start', 'compact-resolved', 'compact-rejected', 'submit-resolved', 'submit-rejected', 'read', 'act-failed',
            'sent', 'send-failed', 'stopped', 'asking', 'asked', 'ask-failed')]
        out['permission_requests'] = [{'agent_id': x['input'].get('agent_id'), 'agent_type': x['input'].get('agent_type'), 'tool': x['input'].get('tool_name'), 'command': (x['input'].get('tool_input') or {}).get('command'), 'hms': x['hms']} for x in self.ev('PermissionRequest')]
        out['notifications'] = [{'agent_id': x['input'].get('agent_id'), 'type': x['input'].get('notification_type'), 'message': x['input'].get('message'), 'hms': x['hms']} for x in self.ev('Notification')]
        out['compaction_hooks'] = [{'ev': x['ev'], 'agent_id': x['input'].get('agent_id'), 'trigger': x['input'].get('trigger'), 'source': x['input'].get('source'), 'hms': x['hms']} for x in recs(self.hooks)
                                   if x['ev'] in ('PreCompact', 'PostCompact') or (x['ev'] == 'SessionStart' and x['input'].get('source') == 'compact')]
        out['boundaries'] = [x.get('input') for x in self.ev('BOUNDARY')]
        out['rewakes'] = [{k: x.get(k) for k in ('waited_ms', 'proof', 'out')} for x in self.ev('rewake')]
        out.update(extra or {})
        for tp in sorted({r['input'].get('transcript_path') for r in recs(self.hooks) if r['input'].get('transcript_path')}):
            if os.path.exists(tp):
                shutil.copy(tp, os.path.join(self.work, 'transcript-' + os.path.basename(tp)))
                sub = os.path.join(tp[:-len('.jsonl')], 'subagents')
                if os.path.isdir(sub):
                    shutil.copytree(sub, os.path.join(self.work, 'subagents'), dirs_exist_ok=True)
        with open(os.path.join(self.work, 'summary.json'), 'w') as f:
            json.dump(out, f, indent=1)
        print(json.dumps(out, indent=1))
        return out


# What the mod does once an agent's call is put to the human, by the n-th such call (ms after the verdict). Keys
# typed while that dialog is up would go to it, so the driver types nothing then but what the row says.
ON_ASK = {
    # (a)-(d): the first prompt of D1 (sh ask.sh), the lead idle: a compaction (whose rewake wakes the lead with
    # its brief), a question asked from the band's render, a prompt submitted with a command a rule allows, reads of
    # the box; the dialog is left open about 3 minutes, past the idle notification's minute
    '1': [[3000, 'read'], [5000, 'compact'], [30000, 'renderask'], [45000, 'submit', 'Run this exact Bash command: pwd. Then reply with only the word PWDONE.'],
          [75000, 'read'], [180000, 'read']],
    # (e): D1's second prompt (sh ask2.sh): the driver types /compact and Enter as serial mode types; the box read
    '2': [[2000, 'read'], [9000, 'read'], [20000, 'read']],
    # (g): D2's first prompt, then the lead's own call put to the human behind it
    '3': [[3000, 'submit', 'Run this exact Bash command: sh lead.sh. Then reply with only the word LEADTWO.']],
}


def D(root):
    r = Run(root, 'D', env={'SPK_DROP_NOTES': '1', 'SPK_REWAKE': '1', 'SPK_ON_ASK': json.dumps(ON_ASK)}, mode='default')
    r.start()
    r.dump('ready', 0.5)
    # (a)-(d)
    r.cmd('/spk-go D1:A')
    r.wait(lambda: r.asked('D1', 'sh ask.sh'), 120, 'D1 asks for sh ask.sh')
    r.dump('a-dialog', 2)
    r.wait(lambda: r.modk('compact-resolved') or r.modk('compact-rejected'), 120, '(a) the compaction settled')
    r.dump('a-compacted', 2)
    r.wait(lambda: r.modk('submit-resolved') or r.modk('submit-rejected'), 90, '(c) the submit settled')
    r.wait(lambda: any('PWDONE' in (x.get('answer') or '') for x in r.modk('turn.complete') if not x.get('agentId')), 90, '(c) the lead turn PWDONE')
    r.dump('c-after', 2)
    r.wait(lambda: len(r.modk('read')) >= 3 and r.ev('Notification', lambda x: x['input'].get('notification_type') == 'idle_prompt'), 150, '(d) idle notification')
    r.wait(lambda: len(r.modk('read')) >= 4, 120, '(d) the last read')
    r.dump('d-before-answer', 1)
    r.mark('(d) answering D1: sh ask.sh')
    r.answer('D1', 'sh ask.sh', 'd')
    r.wait(lambda: r.modk('asking'), 10, 'the band asks')
    if r.modk('asking') and not r.modk('asked'):
        r.dump('d-band-question', 2)
        if 'may it commit unsigned' in r.screen():
            r.key(b'3', '3 for the spk question')
    # (e)
    r.wait(lambda: r.asked('D1', 'sh ask2.sh'), 90, 'D1 asks for sh ask2.sh')
    time.sleep(4)
    r.dump('e-dialog', 0.5)
    r.mark('(e) type /compact, as serial mode types it')
    r.d.type('/compact')
    time.sleep(4)
    r.dump('e-typed', 0.5)
    time.sleep(5)
    r.key(b'\r', 'Enter, as serial mode submits', 6)
    r.dump('e-entered', 1)
    r.wait(lambda: r.ended('D1', 'sh ask2.sh') or r.ev('PreCompact'), 20, '(e) what the keys did')
    if not r.ended('D1', 'sh ask2.sh'):
        r.answer('D1', 'sh ask2.sh', 'e')
    r.wait(lambda: r.turns('D1'), 90, 'D1 done')
    r.d.wait_quiet(3, 30)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    # (f) the lead's own prompt
    r.cmd('Run this exact Bash command: sh lead.sh. Then reply with only the word LEADDONE.')
    r.wait(lambda: r.asked(None, 'sh lead.sh'), 60, '(f) the lead asks for sh lead.sh')
    r.dump('f-dialog', 2)
    r.answer(None, 'sh lead.sh', 'f')
    r.wait(lambda: any('LEADDONE' in (x.get('answer') or '') for x in r.modk('turn.complete') if not x.get('agentId')), 60, '(f) LEADDONE')
    r.d.wait_quiet(3, 30)
    # (g) an agent's prompt, then the lead's behind it: which is in front, and what 1 answers
    n_lead = len(r.asked(None, 'sh lead.sh'))
    r.cmd('/spk-go D2:A')
    r.wait(lambda: r.asked('D2', 'sh ask.sh'), 120, '(g) D2 asks for sh ask.sh')
    r.wait(lambda: len(r.asked(None, 'sh lead.sh')) > n_lead, 60, '(g) the lead asks behind it')
    r.dump('g-both', 3)
    r.mark('(g) 1 for whichever is in front')
    r.key(b'1', '1 (g, first)', 4)
    r.dump('g-after-first', 1)
    r.mark('(g) first 1 ended: D2 ask.sh %s, lead lead.sh %s' % (bool(r.ended('D2', 'sh ask.sh')), len(r.ended(None, 'sh lead.sh')) > 1))
    r.key(b'1', '1 (g, second)', 4)
    r.dump('g-after-second', 1)
    r.wait(lambda: r.asked('D2', 'sh ask2.sh'), 60, '(g) D2 asks for sh ask2.sh')
    r.answer('D2', 'sh ask2.sh', 'g')
    r.wait(lambda: r.turns('D2'), 90, 'D2 done')
    r.d.wait_quiet(3, 30)
    r.dump('end', 1)
    r.finish()
    r.summary()


ON_ASK_E = {
    '1': [[2000, 'read']],
    '2': [[2000, 'read']],
    # (g): E2's first prompt, then the lead's own call put to the human behind it
    '3': [[3000, 'submit', 'Run this exact Bash command: sh lead.sh. Then reply with only the word LEADTWO.']],
}


def E(root):
    """D's (e) to (g) again, with each call's end matched by its id: keys typed into an agent's prompt as serial mode
    types them (/compact and Enter, then a line and Enter); the lead's own prompt; an agent's and the lead's at once."""
    r = Run(root, 'E', env={'SPK_DROP_NOTES': '1', 'SPK_ON_ASK': json.dumps(ON_ASK_E)}, mode='default')
    r.start()
    r.cmd('/spk-go E1:A')
    # (e1) /compact and Enter into E1's first prompt
    r.wait(lambda: r.asked('E1', 'sh ask.sh'), 120, 'E1 asks for sh ask.sh')
    time.sleep(4)
    r.dump('e1-dialog', 0.5)
    r.mark('(e1) type /compact, as serial mode types it')
    r.d.type('/compact')
    time.sleep(3)
    r.dump('e1-typed', 0.5)
    r.mark('(e1) typed: ask.sh ended %s' % bool(r.ended('E1', 'sh ask.sh')))
    r.key(b'\r', 'Enter, as serial mode submits', 6)
    r.dump('e1-entered', 1)
    r.mark('(e1) after Enter: ask.sh ended %s, PreCompact %s' % (bool(r.ended('E1', 'sh ask.sh')), bool(r.ev('PreCompact'))))
    if not r.ended('E1', 'sh ask.sh'):
        r.answer('E1', 'sh ask.sh', 'e1')
    # (e2) one of baton's lines and Enter into E1's second prompt
    r.wait(lambda: r.asked('E1', 'sh ask2.sh'), 60, 'E1 asks for sh ask2.sh')
    time.sleep(4)
    r.dump('e2-dialog', 0.5)
    r.mark('(e2) type a line, as serial mode types baton\'s lines')
    r.d.type('[baton] Reply with only the word BACK.')
    time.sleep(3)
    r.dump('e2-typed', 0.5)
    r.mark('(e2) typed: ask2.sh ended %s' % bool(r.ended('E1', 'sh ask2.sh')))
    r.key(b'\r', 'Enter, as serial mode submits', 6)
    r.dump('e2-entered', 1)
    r.mark('(e2) after Enter: ask2.sh ended %s' % bool(r.ended('E1', 'sh ask2.sh')))
    if not r.ended('E1', 'sh ask2.sh'):
        r.answer('E1', 'sh ask2.sh', 'e2')
    r.wait(lambda: r.turns('E1'), 90, 'E1 done')
    time.sleep(5)
    r.dump('e-after', 0.5)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    # (f) the lead's own prompt
    r.cmd('Run this exact Bash command: sh lead.sh. Then reply with only the word LEADDONE.')
    r.wait(lambda: r.asked(None, 'sh lead.sh'), 60, '(f) the lead asks for sh lead.sh')
    r.dump('f-dialog', 2)
    r.answer(None, 'sh lead.sh', 'f')
    r.wait(lambda: any('LEADDONE' in (x.get('answer') or '') for x in r.modk('turn.complete') if not x.get('agentId')), 60, '(f) LEADDONE')
    time.sleep(5)
    # (g) an agent's prompt, then the lead's behind it: which is in front, and what 1 answers
    n_lead = len(r.asked(None, 'sh lead.sh'))
    r.cmd('/spk-go E2:A')
    r.wait(lambda: r.asked('E2', 'sh ask.sh'), 120, '(g) E2 asks for sh ask.sh')
    r.wait(lambda: len(r.asked(None, 'sh lead.sh')) > n_lead, 60, '(g) the lead asks behind it')
    r.dump('g-both', 3)
    r.mark('(g) 1 for whichever is in front')
    r.key(b'1', '1 (g, first)', 4)
    r.dump('g-after-first', 1)
    r.mark('(g) first 1 ended: E2 ask.sh %s, lead lead.sh %s' % (bool(r.ended('E2', 'sh ask.sh')), len(r.ended(None, 'sh lead.sh')) > 1))
    r.key(b'1', '1 (g, second)', 4)
    r.dump('g-after-second', 1)
    r.mark('(g) second 1 ended: E2 ask.sh %s, lead lead.sh %s' % (bool(r.ended('E2', 'sh ask.sh')), len(r.ended(None, 'sh lead.sh')) > 1))
    r.wait(lambda: r.asked('E2', 'sh ask2.sh'), 60, '(g) E2 asks for sh ask2.sh')
    r.answer('E2', 'sh ask2.sh', 'g')
    r.wait(lambda: r.turns('E2'), 90, 'E2 done')
    time.sleep(5)
    r.dump('end', 1)
    r.finish()
    r.summary()


def X(root):
    """Each way an agent is stopped, one agent each, and a message after (SendMessage, as baton's mod sends)."""
    r = Run(root, 'X', env={'SPK_DROP_NOTES': '1'})
    r.start()
    seen = {}

    def go(name, key='L'):
        r.cmd(f'/spk-go {name}:{key}')
        if key == 'L':
            r.wait(lambda: r.running(name, 'sh long.sh'), 90, name + ' running')
            time.sleep(4)
        else:
            r.wait(lambda: r.turns(name), 90, name + ' done')
            time.sleep(6)
        r.d.wait_quiet(1.5, 20)

    def after(name, label):
        time.sleep(5)
        r.dump(f'{name}-after', 0.5)
        r.unview()
        r.key(CTRL_U, 'ctrl-u: an empty box')
        r.cmd('/spk-list')
        time.sleep(2)
        r.cmd(f'/spk-msg {name} {BACK}')
        r.wait(lambda: [x for x in r.modk('sent') + r.modk('send-failed') if x.get('to') == name], 30, name + ' message answered')
        time.sleep(10)
        seen[name] = {'how': label, 'turns': [x.get('reason') for x in r.turns(name)], 'sent': [x.get('r') or x.get('err') for x in r.modk('sent') + r.modk('send-failed') if x.get('to') == name]}
        r.mark(f'{name} ({label}): {seen[name]}')

    # X1: x on a running agent's row
    go('X1')
    r.select('X1')
    r.dump('X1-selected', 0.5)
    r.key(b'x', 'x on X1 (running)', 2)
    r.key(ESC, 'esc: focus back to the prompt', 1)
    after('X1', 'x on its row, running')
    # X7: x on an idle agent's row
    go('X7', 'Q')
    r.select('X7')
    r.dump('X7-selected', 0.5)
    r.key(b'x', 'x on X7 (idle)', 2)
    r.key(ESC, 'esc: focus back to the prompt', 1)
    after('X7', 'x on its row, idle')
    # X2: ctrl+x ctrl+k at the prompt
    go('X2')
    r.key(CTRL_X, 'ctrl+x', 0.3)
    r.key(CTRL_K, 'ctrl+k', 2)
    r.dump('X2-keys', 0.5)
    if not any(x.get('reason') == 'aborted' for x in r.turns('X2')):
        r.key(CTRL_X, 'ctrl+x again', 0.3)
        r.key(CTRL_K, 'ctrl+k again', 2)
    after('X2', 'ctrl+x ctrl+k')
    # X3: Ctrl-C at the idle prompt, the box empty
    go('X3')
    r.key(CTRL_C, 'ctrl-c at the idle prompt', 2)
    after('X3', 'ctrl-c at the prompt')
    # X4: Esc in the agent's transcript view
    go('X4')
    r.select('X4')
    r.key(b'\r', 'enter: open X4', 2)
    r.dump('X4-view', 0.5)
    r.key(ESC, 'esc in X4 view', 2)
    r.dump('X4-esc', 0.5)
    after('X4', 'esc in its view')
    # X5: the mod's TaskStop, as baton retires an agent
    go('X5')
    r.cmd('/spk-at 500 stop X5')
    r.wait(lambda: r.modk('stopped'), 20, 'X5 stopped')
    after('X5', 'TaskStop from the mod')
    # X6: /clear
    go('X6')
    r.cmd('/clear')
    time.sleep(4)
    after('X6', '/clear')
    r.wait(lambda: r.turns('X6'), 200, 'X6 turn over after /clear')
    r.cmd(f'/spk-msg X6 {BACK}')
    time.sleep(15)
    seen['X6']['sent_later'] = [x.get('r') or x.get('err') for x in r.modk('sent') + r.modk('send-failed') if x.get('to') == 'X6']
    r.cmd('/spk-list')
    r.d.wait_quiet(3, 30)
    r.dump('end', 1)
    r.finish()
    r.summary({'stops': seen, 'lists': [x.get('list') for x in r.modk('list-cmd')]})



def marked(r):
    """Each screen line, with which cells are drawn reversed or on a background colour: how a selection shows."""
    with r.d.lock:
        raw = open(os.path.join(r.repo, 'screen.raw'), 'rb').read()
    scr = pyte.Screen(COLS, ROWS)
    pyte.ByteStream(scr).feed(raw)
    out = []
    for y in range(ROWS):
        line = scr.buffer[y]
        cells = [line[x] for x in range(COLS)]
        text = ''.join(c.data for c in cells).rstrip()
        rev = [x for x, c in enumerate(cells) if c.reverse]
        bg = sorted({c.bg for c in cells if c.bg != 'default'})
        fg = sorted({c.fg for c in cells if c.fg != 'default'})
        bold = [x for x, c in enumerate(cells) if c.bold]
        if text:
            out.append(f'{y:2} rev={rev[:1]}..{len(rev)} bg={bg} fg={fg[:4]} bold={len(bold)} | {text}')
    return '\n'.join(out)


def probe(root):
    """The agent panel's selection: two agents, Down pressed one at a time, each screen with its attributes."""
    r = Run(root, 'probe', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go Y1:L Y2:L')
    r.wait(lambda: r.running('Y1', 'sh long.sh') and r.running('Y2', 'sh long.sh'), 90, 'running')
    time.sleep(3)
    for i in range(4):
        open(os.path.join(r.work, f'marked-{i}.txt'), 'w').write(marked(r))
        r.key(DOWN, 'down', 1.2)
    open(os.path.join(r.work, 'marked-4.txt'), 'w').write(marked(r))
    r.key(ESC, 'esc', 1)
    r.cmd('/spk-at 100 stop Y1')
    r.cmd('/spk-at 100 stop Y2')
    time.sleep(3)
    r.finish()


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
