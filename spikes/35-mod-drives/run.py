#!/usr/bin/env python3
"""Spike 35: what a mod can do in baton's place, and what baton's keys do to phase agents (Haiku).

Usage: run.py <scenario> <workroot>. Spike 32's harness: claude in a PTY in a fresh git repo, the mod (mod/, a copy
per session) and a settings hook on every event (hook.py, spike 32's, logging to hooks.jsonl, with the asyncRewake
PostCompact hook for every trigger); the mod logs to mod.jsonl.*. SPK_CLAUDE names the claude binary (a version under
~/.local/share/claude/versions/), so a scenario runs on 2.1.296 (the incident's) or 2.1.300 (current). Each scenario
keeps a phase agent at work beside the lead, spawned inside a hook so the mod sees its events.

  K   Ctrl-C and Ctrl-U at an idle prompt, with a draft and with an empty box
  Kb  Ctrl-C at an idle prompt while the lead's own background command and subagent run (serial mode)
  C   $.session.compact() from the mod's timer: idle, a draft in the box, during a turn, under a dialog
  S   $.prompt.submit() with a draft in the box, and during a turn
  Su  $.prompt.submit() with asUser, and without
  F   $.prompt.read()/fill(): the stash and restore baton's fallback would make, and fill under a dialog
  P   how agents' reports reach the lead (task-notification or a peer hand-back), and the mod's drop:
      P_auto, P_accept (auto mode, acceptEdits), P_auto_drop (the mod drops peer hand-backs of its agents)
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
          'PermissionRequest', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact',
          'SubagentStart', 'SubagentStop', 'WorktreeCreate', 'WorktreeRemove', 'PermissionDenied']
ROWS, COLS = 50, 160
ESC, CTRL_C, CTRL_U = b'\x1b', b'\x03', b'\x15'
SLOW = 'Run this exact Bash command: sh slow.sh. Then reply with only the word slept.'


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
    open(os.path.join(repo, 'README'), 'w').write('spike\n')
    open(os.path.join(repo, 'slow.sh'), 'w').write('sleep 25\necho slept\n')
    open(os.path.join(repo, 'long.sh'), 'w').write('sleep 60\necho slept long\n')
    open(os.path.join(repo, '.gitignore'), 'w').write('screen.*\n')
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

    def dump(self, label, settle=1.0):
        time.sleep(settle)
        with self.d.lock:
            raw = open(os.path.join(self.repo, 'screen.raw'), 'rb').read()
        scr = pyte.Screen(COLS, ROWS)
        pyte.ByteStream(scr).feed(raw)
        txt = '\n'.join(line.rstrip() for line in scr.display)
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

    def draft(self, text):
        self.mark('draft (no Enter): ' + text)
        self.d.type(text)
        time.sleep(1)

    def key(self, raw, label):
        self.mark('key: ' + label)
        self.d.key(raw)
        time.sleep(0.6)

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

    def lead_running(self, command):
        return self.ev('PreToolUse', lambda x: not x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == command)

    def acts(self, kind):
        return self.modk(kind)

    def finish(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()

    def summary(self, extra=None):
        out = {'claude': self.version}
        out['lead_turns'] = [{k: x.get(k) for k in ('reason', 'answer')} for x in self.modk('turn.complete') if not x.get('agentId')]
        out['agent_turns'] = {n: [x.get('reason') for x in self.turns(n)] for n in sorted({x['name'] for x in self.modk('spawned')})}
        out['acts'] = [{k: v for k, v in x.items() if k not in ('t', 'inst')} for x in self.modrecs() if x['kind'] in (
            'act-start', 'compact-resolved', 'compact-rejected', 'submit-resolved', 'submit-rejected', 'read', 'filled',
            'stashed', 'restored', 'act-failed', 'sent', 'send-failed', 'asked', 'ask-failed')]
        out['prompts'] = [{'origin': x.get('origin'), 'turnId': x.get('turnId'), 'ours': x.get('ours'), 'named': x.get('named'), 'text': (x.get('text') or '')[:120]} for x in self.modk('prompt.submit')]
        out['user_prompt_submit'] = [{'source': x['input'].get('source'), 'agent_id': x['input'].get('agent_id'), 'prompt': (x['input'].get('prompt') or '')[:120]} for x in self.ev('UserPromptSubmit')]
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


def K(root):
    """Ctrl-C and Ctrl-U at an idle prompt while a phase agent runs, as baton's draft rescue and retry rounds type
    them: with a draft in the box, and with it empty. A fresh agent for each case; after each, every agent's state.
    Then a message to each agent that ended: resumed, or refused as stopped by the user?"""
    r = Run(root, 'K', env={'SPK_DROP_NOTES': '1'})
    r.start()
    cases = [('K1', 'draft', CTRL_C, 'ctrl-c'), ('K2', 'empty', CTRL_C, 'ctrl-c'), ('K3', 'draft', CTRL_U, 'ctrl-u'), ('K4', 'empty', CTRL_U, 'ctrl-u')]
    seen = {}
    for name, box, k, label in cases:
        r.cmd(f'/spk-go {name}:W3')
        r.wait(lambda: r.running(name, 'sh long.sh'), 90, name + ' running')
        time.sleep(5)
        r.d.wait_quiet(1.5, 20)
        if box == 'draft':
            r.draft('this is my unsent draft')
        r.dump(name + '-before', 0.5)
        r.key(k, f'{label} with the box {box}, {name} running')
        time.sleep(5)
        r.dump(name + '-after', 0.5)
        r.cmd('/spk-list')
        seen[name] = {n: [x.get('reason') for x in r.turns(n)] for n, *_ in cases if r.agent(n)}
        r.mark(f'after {name}: {seen[name]}')
        time.sleep(3)
    ended = [n for n, *_ in cases if any(x.get('reason') == 'aborted' for x in r.turns(n))]
    r.mark(f'ended: {ended}')
    for n in ended:
        r.cmd(f'/spk-send {n} Use the Write tool to create the file afterkey.txt in your current directory containing exactly the word x. Then reply DONE.')
        time.sleep(12)
    r.cmd('/spk-list')
    r.d.wait_quiet(3, 30)
    r.dump('end', 1)
    r.finish()
    r.summary({'after_each_key': seen, 'ended': ended, 'lists': [x.get('list') for x in r.modk('list-cmd')][-1:]})


def Kb(root):
    """A Ctrl-C at an idle prompt while the lead's own background work runs, as in serial mode (no phase agent): a
    background Bash command and a background subagent the model started. Which of them does it stop?"""
    r = Run(root, 'Kb', env={'SPK_DROP_NOTES': '1'}, allow=('Bash(sleep 90)',))
    r.start()
    r.ask('Run this exact Bash command with run_in_background set to true: sleep 90. Do not wait for it. Then reply with only the word started.')
    r.ask('Use the Agent tool with subagent_type general-purpose and run_in_background set to true, with this prompt: "Run this exact Bash command: sh long.sh. Then reply DONE." Do not wait for it. Then reply with only the word launched.')
    r.wait(lambda: r.ev('PreToolUse', lambda x: x['input'].get('agent_id') and (x['input'].get('tool_input') or {}).get('command') == 'sh long.sh'), 90, 'the model agent running long.sh')
    time.sleep(5)
    r.d.wait_quiet(1.5, 20)
    r.dump('before', 0.5)
    r.key(CTRL_C, 'ctrl-c with the box empty, a background command and agent running')
    time.sleep(5)
    r.dump('after', 0.5)
    r.cmd('/spk-list')
    time.sleep(100)  # the command's 90 s and the agent's 60 s run out, if they still run
    r.d.wait_quiet(3, 60)
    r.dump('end', 1)
    r.finish()
    r.summary()


def C(root):
    """$.session.compact() from the mod's timer, with a phase agent at work: (a) the lead idle, the box empty; (b) a
    draft in the box; (c) while a lead turn runs; (d) while the mod's question is open. Which triggers the settings
    hooks carry, the boundary, the rewake (proof, then a brief that wakes the lead: OSPREY), the draft, the agent."""
    r = Run(root, 'C', env={'SPK_DROP_NOTES': '1', 'SPK_REWAKE': '1'})
    r.start()
    r.cmd('/spk-go C1:W3')
    r.wait(lambda: r.running('C1', 'sh long.sh'), 90, 'C1 running')
    time.sleep(3)
    settled = lambda n: len(r.acts('compact-resolved')) + len(r.acts('compact-rejected')) >= n
    # (a)
    n = r.stops()
    r.cmd('/spk-at 1000 compact')
    r.wait(lambda: settled(1), 240, '(a) compaction settled')
    r.wait(lambda: r.stops() > n, 90, '(a) the lead woken by the rewake')
    r.d.wait_quiet(3, 30)
    r.dump('a-after', 1)
    # (b)
    n = r.stops()
    for ms in (40000, 65000, 90000, 120000):  # queued first: typing a command would take the draft with it
        r.cmd(f'/spk-at {ms} read')
    r.cmd('/spk-at 4000 compact')
    r.draft('my draft during the compaction')
    r.dump('b-draft', 0.5)
    r.wait(lambda: settled(2), 240, '(b) compaction settled')
    r.dump('b-settled', 1)
    r.wait(lambda: r.stops() > n, 90, '(b) the lead woken by the rewake')
    r.wait(lambda: len(r.acts('read')) >= 4, 120, '(b) four reads')
    r.dump('b-after', 1)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    r.d.wait_quiet(3, 30)
    # (c)
    n = r.stops()
    r.cmd('/spk-at 8000 compact')
    r.cmd(SLOW)
    r.wait(lambda: settled(3), 120, '(c) compaction settled')
    r.dump('c-settled', 1)
    r.wait(lambda: r.stops() > n, 120, '(c) the lead turn over')
    time.sleep(20)  # does a rejected compaction come back, or the turn's end start one?
    r.d.wait_quiet(3, 30)
    r.dump('c-after', 1)
    # (d)
    n = r.stops()
    r.cmd('/spk-at 8000 compact')
    r.cmd('/spk-ask 1000')
    r.wait(lambda: r.modk('asking'), 20, '(d) asking')
    time.sleep(20)
    r.dump('d-dialog', 1)
    r.mark('(d) settled while the dialog was open: %s' % settled(4))
    r.key(b'3', '3 for the question')
    r.wait(lambda: settled(4), 120, '(d) compaction settled')
    r.wait(lambda: r.stops() > n, 60, '(d) the lead woken')
    r.d.wait_quiet(3, 30)
    r.dump('d-after', 1)
    r.wait(lambda: r.turns('C1'), 240, 'C1 done')
    r.finish()
    r.summary()


def S(root):
    """$.prompt.submit({text}) from the mod's timer, with a phase agent at work: (a) a draft in the box: a turn of its
    own? the draft kept? what UserPromptSubmit receives; (b) while a lead turn runs."""
    r = Run(root, 'S', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go S1:W3')
    r.wait(lambda: r.running('S1', 'sh long.sh'), 90, 'S1 running')
    n = r.stops()
    for ms in (20000, 40000):  # queued first: typing a command would take the draft with it
        r.cmd(f'/spk-at {ms} read')
    r.cmd('/spk-at 4000 submit Reply with only the word SUBMITTED.')
    r.draft('my draft beside a submit')
    r.wait(lambda: r.stops() > n, 90, '(a) the submitted turn over')
    r.wait(lambda: len(r.acts('read')) >= 2, 60, '(a) reads')
    r.dump('a-after', 1)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    r.d.wait_quiet(3, 30)
    n = r.stops()
    r.cmd('/spk-at 8000 submit Reply with only the word SECOND.')
    r.cmd(SLOW)
    r.wait(lambda: r.stops() >= n + 2, 180, '(b) both turns over')
    r.d.wait_quiet(3, 30)
    r.dump('b-after', 1)
    r.finish()
    r.summary()


def Su(root):
    """$.prompt.submit({text, asUser: true}): what the model, the transcript and UserPromptSubmit see."""
    r = Run(root, 'Su', env={'SPK_DROP_NOTES': '1'})
    r.start()
    n = r.stops()
    r.cmd('/spk-at 1000 submituser Reply with only the word ASUSER.')
    r.wait(lambda: r.stops() > n, 60, 'the asUser turn over')
    n = r.stops()
    r.cmd('/spk-at 1000 submit Reply with only the word FRAMED.')
    r.wait(lambda: r.stops() > n, 60, 'the framed turn over')
    r.d.wait_quiet(3, 30)
    r.finish()
    r.summary()


def F(root):
    """$.prompt.read()/fill() from the mod's timer, with a phase agent at work: (a) the stash and restore baton's
    fallback would make around a typed /compact; (b) fill while the mod's question is open."""
    r = Run(root, 'F', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go F1:W3')
    r.wait(lambda: r.running('F1', 'sh long.sh'), 90, 'F1 running')
    r.cmd('/spk-at 16000 restore')
    r.cmd('/spk-at 8000 stash')
    r.draft('half a thought, with the cursor at its end')
    time.sleep(8)
    r.dump('a-stashed', 0.5)
    r.wait(lambda: r.acts('restored'), 30, 'restored')
    r.dump('a-restored', 1)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    r.cmd('/spk-at 10000 fill FILLED-UNDER-A-DIALOG')
    r.cmd('/spk-at 8000 read')
    r.cmd('/spk-ask 1000')
    r.wait(lambda: r.modk('asking'), 20, 'asking')
    time.sleep(8)
    r.dump('b-dialog', 1)
    r.key(b'3', '3 for the question')
    r.cmd('/spk-at 1000 read')
    time.sleep(3)
    r.dump('b-after', 1)
    r.key(CTRL_U, 'ctrl-u'); r.key(CTRL_U, 'ctrl-u')
    r.d.wait_quiet(3, 30)
    r.finish()
    r.summary()


def _P(root, name, mode, drop):
    """How agents' reports reach the lead: (a) a fresh agent ending while the lead is idle; (b) one ending while a lead
    turn runs; (c) an idle agent resumed by the mod's message; (d) an agent at work for a minute. With the mod's drop
    of task-notifications off, unless `drop` (then it drops both those and peer hand-backs from its agents)."""
    env = {'SPK_DROP_NOTES': '1', 'SPK_DROP_PEER': '1'} if drop else {}
    r = Run(root, name, env=env, mode=mode)
    r.start()
    quiet = lambda: r.d.wait_quiet(4, 60)
    r.cmd('/spk-go A1:Q')
    r.wait(lambda: r.turns('A1'), 90, '(a) A1 done')
    time.sleep(20)
    quiet()
    r.mark('(b)')
    r.cmd('/spk-at 6000 spawn A2:Q')
    r.cmd(SLOW)
    r.wait(lambda: r.turns('A2'), 90, '(b) A2 done')
    r.wait(lambda: r.lead_running('sh slow.sh'), 60, '(b) the lead running slow.sh')
    time.sleep(40)
    quiet()
    r.mark('(c)')
    k = len(r.turns('A1'))
    r.cmd('/spk-send A1 Use the Write tool to create the file again.txt in your current directory containing exactly the word again. Then reply DONE again.')
    r.wait(lambda: len(r.turns('A1')) > k, 90, '(c) A1 resumed and done')
    time.sleep(20)
    quiet()
    r.mark('(d)')
    r.cmd('/spk-go A3:W')
    r.wait(lambda: r.turns('A3'), 150, '(d) A3 done')
    time.sleep(20)
    quiet()
    r.dump('end', 1)
    r.finish()
    r.summary()


def P_auto(root):
    _P(root, 'P_auto', 'auto', False)


def P_accept(root):
    _P(root, 'P_accept', 'acceptEdits', False)


def P_auto_drop(root):
    _P(root, 'P_auto_drop', 'auto', True)


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
