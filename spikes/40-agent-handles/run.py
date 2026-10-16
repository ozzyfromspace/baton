#!/usr/bin/env python3
"""Spike 40: writing to a phase agent from the lead's prompt (Claude Code 2.1.301, Haiku, acceptEdits).

Usage: run.py H <workroot>. Spike 36's harness: claude in a PTY (raw bytes kept, rendered through pyte) in a fresh git
repo, with no user settings (--setting-sources project,local), the mod (mod/, copied per run; it drops agents' reports
to the lead, as baton's mod does) and hook.py on every settings event, logging to hooks.jsonl; the mod logs to
mod.jsonl.*. hook.py keeps a prompt `@<name> <words>` from the model at UserPromptSubmit, as baton's lead hook would.
SPK_CLAUDE names the claude binary. The steps, in one session:

  a   the typeahead on `@P`; `@P1 <words>` blocked three ways (decision block; with a systemMessage; continue false):
      the screen, whether the box keeps the text (Enter again), what the mod and the lead's model get; then the box
      emptied by the mod after a block ($.prompt.fill, from a timer, only while it holds that prompt)
  a4  the same while the lead's turn runs a 25-second command
  b   the mod's SendMessage to a running agent, to it once completed, and to one the human stopped with x; then the
      human typing in the stopped agent's view
  c   an agent that committed in its worktree, its view open; once it is done its worktree is removed (as baton
      retires one), then the human types in its view, then the mod sends it a message
  d   a second agent spawned under the name of a running one: the agent list, the typeahead, and a SendMessage to
      the name
  e   from the lead's transcript and the mod's turns: what the blocked prompts cost
"""
import glob, json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver, ANSI
import pyte  # the spike runs under a venv that has it

HOOK = os.path.join(HERE, 'hook.py')
MOD = os.path.join(HERE, 'mod')
CLAUDE = os.environ.get('SPK_CLAUDE') or 'claude'
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'Notification', 'Stop', 'StopFailure', 'SubagentStart', 'SubagentStop']  # no WorktreeCreate: a hook there replaces Claude Code's own
ROWS, COLS = 50, 160
ESC, DOWN, CTRL_U = b'\x1b', b'\x1b[B', b'\x15'
ALLOW = ('Bash(pwd)', 'Bash(sh slow.sh)', 'Bash(sh long.sh)', 'Bash(sh commit.sh)', 'Bash(echo FROM-MOD)')


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
    return subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, capture_output=True, text=True)


def setup(work):
    shutil.rmtree(work, ignore_errors=True)
    repo = os.path.join(work, 'repo')
    os.makedirs(os.path.join(repo, '.claude'))
    git(repo, 'init', '-q', '-b', 'main')
    files = {'README': 'spike 40\n', 'slow.sh': 'sleep 25\necho slept\n', 'long.sh': 'sleep 60\necho slept long\n',
             'commit.sh': 'echo phase > phase.txt && git add phase.txt && git -c user.name=s -c user.email=s@s commit -qm phase && echo committed\n',
             '.gitignore': 'screen.*\nplans/\n', '.claude/settings.json': json.dumps({'plansDirectory': 'plans'})}
    for name, text in files.items():
        open(os.path.join(repo, name), 'w').write(text)
    git(repo, 'add', '.')
    git(repo, 'commit', '-qm', 'init')
    return repo


def settings(work):
    log = shlex.quote(os.path.join(work, 'hooks.jsonl'))
    return json.dumps({'hooks': {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {log}'}]}] for ev in EVENTS}})


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


class Run:
    def __init__(self, root, name):
        self.work = os.path.join(root, name)
        self.repo = setup(self.work)
        mod = os.path.join(self.work, 'mod')
        shutil.copytree(MOD, mod, ignore=shutil.ignore_patterns('types', 'tsconfig.json'))
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.mod = os.path.join(self.work, 'mod.jsonl')
        argv = [CLAUDE, '--model', 'haiku', '--setting-sources', 'project,local', '--permission-mode', 'acceptEdits',
                '--plugin-dir', mod, '--settings', settings(self.work)]
        for a in ALLOW:
            argv += ['--allowedTools', a]
        self.d = RawDriver(self.repo, argv, env_extra={'SPK_LOG': self.mod, 'SPK_SCEN': name + str(int(time.time()))}, rows=ROWS, cols=COLS)
        self.version = subprocess.run([CLAUDE, '--version'], capture_output=True, text=True).stdout.strip()
        self.notes = {}

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

    def type(self, text):
        self.mark('type: ' + text[:120])
        self.d.type(text)
        time.sleep(0.6)

    def cmd(self, text):
        self.type(text)
        self.d.enter()
        time.sleep(1.5)

    def key(self, raw, label, settle=0.6):
        self.mark('key: ' + label)
        self.d.key(raw)
        time.sleep(settle)

    def stops(self):
        return len(self.ev('Stop', lambda x: not x['input'].get('agent_id')))

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

    def tool(self, name, command):
        aid = self.agent(name)
        return [x for x in self.ev('PreToolUse') if aid and x['input'].get('agent_id') == aid and (x['input'].get('tool_input') or {}).get('command') == command]

    def sent(self, to):
        return [x.get('r') or x.get('err') for x in self.modk('sent') + self.modk('send-failed') if x.get('to') == to]

    def unview(self):
        """Back to the main conversation, if an agent's transcript view is open."""
        for i in range(3):
            if 'Message @' not in self.screen():
                return
            self.key(ESC, 'esc: back to the main conversation', 1.5)

    def select(self, name):
        """Focus the agent panel and select name's row: the selected row starts with ❯ (spike 36)."""
        for i in range(6):
            if any(l.startswith('❯ ') and f' {name} ' in l for l in self.screen().splitlines()):
                return True
            self.key(DOWN, 'down', 0.8)
        self.dump(f'select-{name}', 0.5)
        return False

    def submits(self):
        return self.ev('UserPromptSubmit', lambda x: not x['input'].get('agent_id'))

    def lead_transcript(self):
        for x in self.submits():
            tp = x['input'].get('transcript_path')
            if tp:
                return tp

    def finish(self):
        self.unview()
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()


def blocked(r, label, text):
    """Type `text` at the lead's prompt and Enter; then Enter again, which re-submits only if the box kept the text."""
    n = len(r.submits())
    r.type(text)
    r.dump(f'{label}-typed', 0.3)
    r.mark(f'{label}: enter')
    r.d.enter()
    r.wait(lambda: len(r.submits()) > n, 20, f'{label}: UserPromptSubmit')
    after = r.dump(f'{label}-after', 3)
    r.key(b'\r', f'{label}: enter again', 3)
    again = len(r.submits()) - n - 1
    r.dump(f'{label}-enter-again', 0.5)
    r.key(CTRL_U, f'{label}: ctrl-u', 0.8)
    r.notes[label] = {'submits': len(r.submits()) - n, 'resubmitted_by_enter': again, 'box_line': [l for l in after.splitlines() if l.lstrip().startswith(('>', '❯'))][-3:]}
    r.mark(f'{label}: {r.notes[label]}')


def H(root):
    r = Run(root, 'H')
    r.start()

    # a: the typeahead, then `@P1 <words>` blocked three ways, while P1 runs
    r.cmd('/spk-go P1:L')
    r.wait(lambda: r.tool('P1', 'sh long.sh'), 90, 'P1 running')
    r.type('@P')
    r.dump('a0-typeahead', 1.5)
    r.key(ESC, 'esc: close the typeahead', 0.8)
    r.key(CTRL_U, 'ctrl-u', 0.8)
    r.dump('a0-cleared', 0.3)
    blocked(r, 'a1', '@P1 please write a file named from-human.txt')
    blocked(r, 'a2', '@P1 [sys] note two from the human')
    blocked(r, 'a3', '@P1 [cont] note three from the human')
    blocked(r, 'a5', '@P1 [clear] note five from the human')

    # a4: the same while the lead's turn runs a command
    n = r.stops()
    r.cmd('Run this exact Bash command: sh slow.sh. Then reply with only the word SLOWDONE.')
    r.wait(lambda: r.ev('PreToolUse', lambda x: not x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == 'sh slow.sh'), 60, 'the lead runs slow.sh')
    time.sleep(3)
    r.type('@P1 note four while the lead works')
    r.d.enter()
    r.dump('a4-queued', 3)
    r.wait(lambda: r.stops() > n, 90, 'the lead stops')
    r.dump('a4-after', 3)
    r.key(b'\r', 'a4: enter again', 3)
    r.key(CTRL_U, 'a4: ctrl-u', 0.8)

    # b: the mod's message to a running agent, to it once completed, to one the human stopped
    r.cmd('/spk-msg P1 Also run this exact Bash command now, before your next step: echo FROM-MOD')
    r.wait(lambda: r.sent('P1'), 30, 'P1 message answered')
    r.wait(lambda: r.tool('P1', 'echo FROM-MOD'), 90, 'P1 runs echo FROM-MOD')
    r.wait(lambda: r.turns('P1'), 240, 'P1 done')
    time.sleep(5)
    r.cmd('/spk-msg P1 Reply with only the word BACK.')
    r.wait(lambda: len(r.sent('P1')) >= 2, 30, 'P1 second message answered')
    r.wait(lambda: len(r.turns('P1')) >= 2, 60, 'P1 resumed and done')
    r.cmd('/spk-go P2:L')
    r.wait(lambda: r.tool('P2', 'sh long.sh'), 90, 'P2 running')
    r.select('P2')
    r.dump('b-P2-selected', 0.5)
    r.key(b'x', 'x on P2 (running)', 2)
    r.key(ESC, 'esc: focus back to the prompt', 1)
    r.cmd('/spk-msg P2 Reply with only the word BACK.')
    r.wait(lambda: r.sent('P2'), 30, 'P2 message answered')
    r.select('P2')
    r.key(b'\r', 'enter: open P2', 2)
    r.dump('b-P2-view', 0.5)
    k = len(r.turns('P2'))
    r.type('Reply with only the word HUMANBACK.')
    r.d.enter()
    r.wait(lambda: len(r.turns('P2')) > k, 60, 'P2 resumed by the human')
    r.dump('b-P2-view-after', 2)
    r.unview()

    # c: an agent done, its worktree removed, the human types in its view, then the mod sends it a message
    r.cmd('/spk-go P3:C')
    r.wait(lambda: r.tool('P3', 'sh slow.sh'), 90, 'P3 running slow.sh')
    r.select('P3')
    r.key(b'\r', 'enter: open P3', 2)
    r.dump('c-P3-view', 0.5)
    r.wait(lambda: r.turns('P3'), 90, 'P3 done')
    r.dump('c-P3-done', 3)
    commits = r.tool('P3', 'sh commit.sh')
    wt = commits[0]['input'].get('cwd') if commits else None
    aid = r.agent('P3')
    meta = glob.glob(os.path.join(os.path.dirname(r.lead_transcript() or ''), '*', 'subagents', f'agent-{aid}.meta.json'))
    r.notes['c_meta_before'] = json.load(open(meta[0])) if meta else None
    rm = git(r.repo, 'worktree', 'remove', '--force', wt) if wt else None
    r.notes['c_worktree'] = {'dir': wt, 'removed': rm and rm.returncode == 0, 'err': rm and rm.stderr, 'exists': bool(wt) and os.path.exists(wt)}
    r.mark(f'c: worktree removed {r.notes["c_worktree"]}')
    k, h = len(r.turns('P3')), len(recs(r.hooks))
    r.type('Run this exact Bash command: pwd. Then reply with only its output.')
    r.d.enter()
    r.wait(lambda: len(r.turns('P3')) > k, 45, 'P3 resumed by the human')
    r.dump('c-P3-typed', 3)
    r.notes['c_hooks_after'] = [{'ev': x['ev'], 'cwd': x['input'].get('cwd'), 'tool': x['input'].get('tool_name'), 'command': (x['input'].get('tool_input') or {}).get('command'),
                                 'response': (x['input'].get('tool_response') or {}) if x['ev'] == 'PostToolUse' else None}
                                for x in recs(r.hooks)[h:] if x['input'].get('agent_id') == aid]
    r.notes['c_meta_after'] = json.load(open(meta[0])) if meta else None
    r.unview()
    r.cmd('/spk-msg P3 Reply with only the word BACK.')
    r.wait(lambda: r.sent('P3'), 30, 'P3 message answered')
    time.sleep(8)

    # d: a second agent named P4 (in run 1 the lead's model was asked to name one: its Agent tool has no name), while P4 runs
    r.cmd('/spk-go P4:L')
    r.wait(lambda: r.tool('P4', 'sh long.sh'), 90, 'P4 running')
    r.cmd('/spk-go P4:Q')
    r.wait(lambda: r.turns('P4#2'), 90, 'the second P4 done')
    time.sleep(3)
    r.cmd('/spk-list')
    r.type('@P4')
    r.dump('d-typeahead', 1.5)
    r.key(ESC, 'esc: close the typeahead', 0.8)
    r.key(CTRL_U, 'ctrl-u', 0.8)
    r.cmd('/spk-raw P4 Reply with only the word WHICH.')
    r.wait(lambda: r.sent('P4'), 30, 'P4 raw message answered')
    time.sleep(15)
    r.cmd('/spk-stop P4')
    time.sleep(3)
    r.dump('end', 1)
    tp = r.lead_transcript()
    r.finish()

    # e: what the blocked prompts left in the lead's transcript
    words = ['from-human.txt', 'note two', 'note three', 'note four']
    lines = []
    if tp and os.path.exists(tp):
        for l in open(tp):
            if any(w in l for w in words):
                try:
                    e = json.loads(l)
                except ValueError:
                    continue
                lines.append({'type': e.get('type'), 'subtype': e.get('subtype'), 'isSidechain': e.get('isSidechain'), 'isMeta': e.get('isMeta'),
                              'text': json.dumps(e.get('message') or e.get('content') or e.get('attachment'))[:400]})
    out = {'claude': r.version, 'notes': r.notes, 'transcript': tp, 'transcript_lines_with_the_words': lines}
    out['submits'] = [{'hms': x['hms'], 'prompt': x['input'].get('prompt'), 'keys': sorted(x['input']), 'out': x.get('out')} for x in r.submits()]
    out['mod_prompts'] = [{k: x.get(k) for k in ('t', 'kind', 'origin', 'turnId', 'wait', 'isMeta', 'text', 'r')} for x in r.modrecs() if x['kind'] in ('prompt.submit', 'prompt.submitted')]
    out['lead_turns'] = [{k: x.get(k) for k in ('t', 'kind', 'turnId', 'reason', 'answer')} for x in r.modrecs() if x['kind'] in ('turn.start', 'turn.complete') and not x.get('agentId')]
    out['agent_turns'] = {n: [{k: x.get(k) for k in ('t', 'reason', 'answer')} for x in r.turns(n)] for n in ('P1', 'P2', 'P3', 'P4', 'P4#2')}
    out['sent'] = [{k: x.get(k) for k in ('to', 'target', 'raw', 'ms', 'r', 'err')} for x in r.modk('sent') + r.modk('send-failed')]
    out['lists'] = [x.get('list') for x in r.modk('list-cmd')]
    out['subagent_starts'] = [{'hms': x['hms'], 'agent_id': x['input'].get('agent_id'), 'agent_type': x['input'].get('agent_type'), 'cwd': x['input'].get('cwd')} for x in r.ev('SubagentStart')]
    out['agents'] = {n: r.agent(n) for n in ('P1', 'P2', 'P3', 'P4', 'P4#2')}
    out['caught'] = r.modk('caught')
    json.dump(out, open(os.path.join(r.work, 'summary.json'), 'w'), indent=1)
    print(json.dumps(out, indent=1))


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
