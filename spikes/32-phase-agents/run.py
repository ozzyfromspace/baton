#!/usr/bin/env python3
"""Spike 32: what baton's bridge and phase agents still need to know (Claude Code 2.1.296, Haiku).

Usage: run.py <scenario> <workroot>. As spike 31: claude in a PTY in a fresh git repo, with the mod (mod/) and a
settings hook on every event (hook.py, logging to hooks.jsonl; the mod logs to mod.jsonl.*). New: the PTY's raw
bytes are kept (screen.raw), so a step's screen renders as drawn (screen-<label>.txt, via pyte); PostCompact also
has an asyncRewake hook for every trigger (hook.py rewake); and the API can go through proxy.py, which fails one
agent's requests on demand.
"""
import glob, json, os, shlex, shutil, signal, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver, ANSI
import pyte  # the spike runs under a venv that has it

HOOK = os.path.join(HERE, 'hook.py')
# Each session loads its own copy of the mod (<work>/mod): a mod reloads when its file is saved, so the source
# can then be edited while scenarios run. SPK_MOD_SRC names another source.
MOD = os.environ.get('SPK_MOD_SRC') or os.path.join(HERE, 'mod')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact',
          'SubagentStart', 'SubagentStop', 'WorktreeCreate', 'WorktreeRemove', 'PermissionDenied']
ROWS, COLS = 50, 160
ESC, UP, DOWN, CTRL_C, CTRL_U = b'\x1b', b'\x1b[A', b'\x1b[B', b'\x03', b'\x15'


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
    words = 'alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa'.split()
    for i in range(1, 5):  # about 25k tokens each: four of them push a small compaction window over
        open(os.path.join(repo, f'big{i}.txt'), 'w').write('\n'.join(' '.join(words[(j + k) % 16] for k in range(12)) + f' {i}.{j}' for j in range(1500)) + '\n')
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
    def __init__(self, root, name, env=None, resume=None, reuse=False, allow=()):
        self.work = os.path.join(root, name)
        self.repo = os.path.join(self.work, 'repo') if reuse else setup(self.work)
        mod = self.moddir = os.path.join(self.work, 'mod')
        if not reuse:
            shutil.copytree(MOD, mod, ignore=shutil.ignore_patterns('types', 'tsconfig.json'))
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.mod = os.path.join(self.work, 'mod.jsonl')
        argv = ['claude', '--model', 'haiku', '--permission-mode', 'acceptEdits', '--plugin-dir', mod, '--settings', settings(self.work)]
        if resume:
            argv += ['--resume', resume]
        for a in ('Bash(pwd)', 'Bash(sh slow.sh)', 'Bash(sh long.sh)') + tuple(allow):
            argv += ['--allowedTools', a]
        e = {'SPK_LOG': self.mod, 'SPK_SCEN': name + str(int(time.time()))}
        e.update(env or {})
        self.d = RawDriver(self.repo, argv, env_extra=e, rows=ROWS, cols=COLS)
        self.t0 = time.time()

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
        # from the settings hooks: the mod does not see the tool calls of an agent it spawned from a timer (S0)
        aid = self.agent(name)
        return aid and self.ev('PreToolUse', lambda x: x['input'].get('agent_id') == aid and (x['input'].get('tool_input') or {}).get('command') == command)

    def wt(self, name):
        aid = self.agent(name)
        return os.path.join(self.work, 'bwt-agent-' + aid) if aid else None

    def has(self, name, f):
        w = self.wt(name)
        return bool(w) and os.path.exists(os.path.join(w, f))

    def finish(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()

    def summary(self, extra=None):
        out = {'files': {}}
        for where in [self.repo] + sorted(glob.glob(os.path.join(self.work, 'bwt-*'))):
            for f in sorted(os.listdir(where)):
                if f.endswith('.txt') and not f.startswith('big'):
                    p = os.path.join(where, f)
                    out['files'][os.path.relpath(p, self.work)] = [open(p).read().strip()[:80], f'{os.path.getmtime(p):.3f}']
        out['sessions'] = sorted({r['input'].get('session_id') for r in recs(self.hooks) if r['input'].get('session_id')})
        out['hook_events_by_agent'] = {}
        for r in recs(self.hooks):
            if r['ev'] in ('DRIVER', 'BOUNDARY', 'rewake'):
                continue
            k = r['input'].get('agent_type', '') + '/' + (r['input'].get('agent_id') or 'main')[:10]
            out['hook_events_by_agent'].setdefault(k, []).append(r['ev'] + (':' + r['input'].get('tool_name') if r['input'].get('tool_name') else ''))
        for k, v in out['hook_events_by_agent'].items():
            out['hook_events_by_agent'][k] = ' '.join(v)
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


def S0(root):
    """Which of a phase agent's events reach the mod, by where the mod spawned it: from a timer (as a command poll
    would), inside a command.run hook, or inside a ui.render hook that a timer's $.ui.invalidate asks for. (S0-try1
    also tried a timer that runs the spawning command through $.command.run: the mod's own command.run hook is
    skipped, and nothing spawns.) Each fills a small window, so its compaction shows too. Then a message to each, by
    the three routes."""
    r = Run(root, 'S0', env={'SPK_DROP_NOTES': '1', 'CLAUDE_AUTOCOMPACT_PCT_OVERRIDE': '25'})
    r.start()
    for n, how in (('O1', '@timer'), ('O2', '@hook'), ('O4', '@render')):
        r.cmd(f'/spk-go {how} {n}:BIG')
        r.wait(lambda: r.agent(n), 30, n + ' spawned')
    r.wait(lambda: all(r.turns(n) for n in ('O1', 'O2', 'O4') if r.agent(n)), 600, 'all turns over')
    r.d.wait_quiet(3, 30)
    for n, how in (('O1', '@hook '), ('O2', ''), ('O4', '@render ')):
        if not r.agent(n):
            continue
        k = len(r.turns(n))
        r.cmd(f'/spk-send {how}{n} Use the Write tool to create the file again.txt in your current directory containing exactly the word again. Then reply DONE.')
        r.wait(lambda: len(r.turns(n)) > k, 120, n + ' resumed')
    r.d.wait_quiet(3, 30)
    r.finish()
    import collections
    seen = {}
    for n in ('O1', 'O2', 'O4'):
        aid = r.agent(n)
        if not aid:
            continue
        mod = [x for x in r.modrecs() if x.get('agentId') == aid or x.get('agent_id') == aid]
        seen[n] = {'id': aid, 'how': r.modk('spawned', lambda x: x['name'] == n)[-1].get('how'),
                   'mod': dict(collections.Counter(x['kind'] + ('/' + x['phase'] if x.get('phase') else '') for x in mod))}
    seen['caught'] = dict(collections.Counter(str((x.get('kind'), x.get('ev') or x.get('tool') or x.get('trigger') or '')) for x in r.modk('caught')))
    r.summary({'by_agent': seen})


def S0b(root):
    """The way a command poll would spawn: a timer queues the spawn and calls $.ui.invalidate; the band's render hook
    spawns. With nothing open, with the mod's own question open, and with /tasks open: when does the render (and the
    spawn) happen, and does the mod then see the agent's tool calls?"""
    r = Run(root, 'S0b', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go @rtimer:1000 O5:Q')
    r.wait(lambda: r.turns('O5'), 60, 'O5 done')
    r.cmd('/spk-go @rtimer:6000 O6:Q')
    r.cmd('/spk-ask 300')
    r.wait(lambda: r.modk('asking'), 20, 'asking')
    r.dump('ask-open', 1)
    time.sleep(12)
    r.dump('ask-open-later', 1)
    r.key(b'3', '3 for the question')
    r.wait(lambda: r.turns('O6'), 60, 'O6 done')
    r.cmd('/spk-go @rtimer:6000 O7:Q')
    r.cmd('/tasks')
    r.dump('tasks-open', 2)
    time.sleep(12)
    r.dump('tasks-open-later', 1)
    r.key(ESC, 'esc')
    r.key(ESC, 'esc')
    r.wait(lambda: r.turns('O7'), 60, 'O7 done')
    r.d.wait_quiet(3, 20)
    r.finish()
    import collections
    seen = {n: dict(collections.Counter(x['kind'] for x in r.modrecs() if r.agent(n) and x.get('agentId') == r.agent(n))) for n in ('O5', 'O6', 'O7')}
    t = {k: [round(x['t'] / 1000, 2) for x in r.modk(k)] for k in ('invalidate', 'render-run', 'spawned', 'asking', 'asked')}
    r.summary({'by_agent': seen, 'times': t})


def S1(root):
    """Messages to a running agent (the reserved $.session.send, and the SendMessage tool from the mod), to an idle
    one, and to one the mod stopped with TaskStop."""
    r = Run(root, 'S1', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go P1:W P2:W')
    r.wait(lambda: r.running('P1', 'sh long.sh') and r.running('P2', 'sh long.sh'), 90, 'both running long.sh')
    time.sleep(6)
    r.cmd('/spk-send P1 baton says: use the Write tool to create the file sendA.txt in your current directory containing exactly the word a. Then go on with your task.')
    time.sleep(3)
    r.cmd('/spk-msg P1 baton says: use the Write tool to create the file sendB.txt in your current directory containing exactly the word b. Then go on with your task.')
    time.sleep(3)
    r.cmd('/spk-stop P2')
    r.dump('sent-and-stopped', 2)
    r.wait(lambda: r.turns('P1'), 150, 'P1 turn over')
    r.wait(lambda: r.has('P1', 'sendA.txt') and r.has('P1', 'sendB.txt'), 60, 'sendA and sendB')
    time.sleep(5)
    r.dump('p1-idle', 1)
    n = len(r.turns('P1'))
    r.cmd('/spk-msg P1 Use the Write tool to create the file sendC.txt in your current directory containing exactly the word c. Then reply DONE.')
    r.wait(lambda: len(r.turns('P1')) > n, 90, 'P1 resumed by SendMessage')
    r.cmd('/spk-list')
    r.cmd('/spk-send P2 Use the Write tool to create the file sendD.txt in your current directory containing exactly the word d. Then reply DONE.')
    r.wait(lambda: r.has('P2', 'sendD.txt'), 60, 'P2 resumed by session.send after TaskStop')
    time.sleep(3)
    r.cmd('/spk-msg P2 Use the Write tool to create the file sendE.txt in your current directory containing exactly the word e. Then reply DONE.')
    r.wait(lambda: r.has('P2', 'sendE.txt'), 60, 'P2 resumed by SendMessage after TaskStop')
    r.d.wait_quiet(3, 30)
    r.cmd('/spk-list')
    r.dump('end', 1)
    r.finish()
    r.summary()


def S2(root):
    """A phase agent compacts by itself (small window): the mod's classic.* events and a stamp the settings hooks may
    receive, the order of the mod's session.compact against the settings hooks, a message sent during its compaction,
    the asyncRewake hook (no proof in the lead's transcript: no wake). Then the lead's own /compact: the boundary's
    timing, the stamp, and the rewake with a long brief."""
    r = Run(root, 'S2', env={'SPK_DROP_NOTES': '1', 'SPK_STAMP': '1', 'SPK_SEND_ON_COMPACT': '1', 'SPK_REWAKE': '1', 'CLAUDE_AUTOCOMPACT_PCT_OVERRIDE': '25'})
    r.start()
    r.cmd('/spk-go @hook P5:BIG')  # inside a hook, so the mod sees the agent's compaction (S0)
    r.wait(lambda: r.modk('session.compact', lambda x: x.get('agentId') and x['phase'] == 'after'), 420, 'P5 compacted')
    r.wait(lambda: r.turns('P5'), 300, 'P5 done')
    r.wait(lambda: r.has('P5', 'during-compact.txt'), 120, 'the message sent during compaction done')
    r.d.wait_quiet(3, 30)
    r.dump('p5-done', 1)
    time.sleep(25)  # let the agent's rewake wait time out (20 s): no proof, no wake
    n = r.stops()
    r.cmd('/compact')
    r.wait(lambda: r.ev('rewake', lambda x: x.get('proof')), 180, 'the lead rewake')
    r.wait(lambda: r.stops() > n, 120, 'the lead woken')
    r.d.wait_quiet(3, 30)
    r.dump('lead-woken', 1)
    r.finish()
    r.summary()


def S3(root):
    """A typed /compact in the lead while two phase agents run: they go on, and the lead's Stop lists them in
    background_tasks; the rewake (proven by the lead's boundary) wakes the lead with the brief."""
    r = Run(root, 'S3', env={'SPK_DROP_NOTES': '1', 'SPK_REWAKE': '1'})
    r.start()
    r.cmd('/spk-go P1:W2 P2:W2')
    r.wait(lambda: r.running('P1', 'sh long.sh') and r.running('P2', 'sh long.sh'), 90, 'both running')
    time.sleep(5)
    n = r.stops()
    r.cmd('/compact')
    r.dump('compacting', 3)
    r.wait(lambda: r.ev('PostCompact', lambda x: not x['input'].get('agent_id') and x['input'].get('trigger') == 'manual'), 180, 'lead compacted')
    r.wait(lambda: r.stops() > n, 120, 'the lead woken')
    r.dump('woken', 2)
    r.wait(lambda: r.has('P1', 'P1.txt') and r.has('P2', 'P2.txt'), 300, 'both agents done')
    r.d.wait_quiet(3, 30)
    r.finish()
    r.summary()


def S4(root):
    """The lead compacts by itself mid-turn (small window); the rewake waits for the proof and wakes it with a long
    brief: delivered when?"""
    r = Run(root, 'S4', env={'SPK_REWAKE': '1', 'CLAUDE_AUTOCOMPACT_PCT_OVERRIDE': '25'})
    r.start()
    r.cmd('Use the Read tool to read big1.txt, then big2.txt, then big3.txt, then big4.txt, one at a time and in full. Then reply with only the word FINISHED.')
    r.wait(lambda: r.ev('PostCompact', lambda x: x['input'].get('trigger') == 'auto'), 420, 'auto compaction')
    r.wait(lambda: r.ev('rewake'), 60, 'rewake ran')
    r.wait(lambda: any('OSPREY' in (x.get('answer') or '') for x in r.modk('turn.complete')), 240, 'OSPREY')
    r.d.wait_quiet(5, 60)
    r.dump('end', 1)
    r.finish()
    r.summary()


def S5(root):
    """Esc, Ctrl-C, a rewind and /clear in the lead, each while a phase agent runs: what happens to it, and whether a
    message by id reaches it afterwards."""
    r = Run(root, 'S5', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go A1:W2')
    r.wait(lambda: r.running('A1', 'sh long.sh'), 90, 'A1 running')
    for label, k in (('esc', ESC), ('ctrl-c', CTRL_C)):
        r.cmd('Run this exact Bash command: sh slow.sh. Then reply with only the word slept.')
        r.wait(lambda: r.ev('PreToolUse', lambda x: not x['input'].get('agent_id') and 'slow.sh' in json.dumps(x['input'].get('tool_input'))), 60, 'lead running slow.sh')
        time.sleep(3)
        r.key(k, label + ' in the lead turn')
        time.sleep(5)
        r.cmd('/spk-list')
        r.dump('after-' + label, 1)
    r.mark('rewind: Esc Esc, Up, Enter, 2')
    r.d.key(ESC); time.sleep(0.3); r.d.key(ESC); time.sleep(1.5)
    r.dump('rewind-menu', 0.5)
    r.d.key(UP); time.sleep(0.6); r.d.key(b'\r'); time.sleep(1.5)
    r.dump('rewind-options', 0.5)
    r.d.key(b'2'); time.sleep(2)
    r.d.key(CTRL_U); time.sleep(0.3); r.d.key(CTRL_U); time.sleep(1)
    r.dump('after-rewind', 1)
    r.cmd('/spk-list')
    r.cmd('/spk-go A2:W')
    r.wait(lambda: r.running('A2', 'sh long.sh'), 90, 'A2 running')
    r.cmd('/clear')
    time.sleep(4)
    r.cmd('/spk-list')
    r.dump('after-clear', 1)
    r.wait(lambda: r.has('A2', 'A2.txt'), 150, 'A2 done after /clear')
    r.wait(lambda: r.has('A1', 'A1.txt'), 200, 'A1 done')
    time.sleep(5)
    r.cmd('/spk-send A1 Use the Write tool to create the file afterclear.txt in your current directory containing exactly the word x. Then reply DONE.')
    r.wait(lambda: r.has('A1', 'afterclear.txt'), 90, 'A1 resumed after /clear')
    r.cmd('/spk-msg A2 Use the Write tool to create the file afterclear2.txt in your current directory containing exactly the word y. Then reply DONE.')
    r.wait(lambda: r.has('A2', 'afterclear2.txt'), 90, 'A2 resumed by SendMessage after /clear')
    r.d.wait_quiet(3, 30)
    r.finish()
    r.summary()


def S5b(root):
    """S5 again for a rewind and /clear, each while phase agents run: a rewind that restores the conversation
    (option 1; with no code changes option 2 is "Summarize from here", which S5 picked), then /clear. Do the agents
    go on, and does a message by id reach them afterwards?"""
    r = Run(root, 'S5b', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go R1:W2')
    r.wait(lambda: r.running('R1', 'sh long.sh'), 90, 'R1 running')
    r.ask('Reply with only the word one.')
    r.mark('rewind: Esc Esc, Enter on the last prompt, 1 (restore conversation)')
    r.d.key(ESC); time.sleep(0.3); r.d.key(ESC); time.sleep(1.5)
    r.dump('rewind-menu', 0.5)
    r.d.key(UP); time.sleep(0.6); r.d.key(b'\r'); time.sleep(1.5)
    r.dump('rewind-options', 0.5)
    r.d.key(b'1'); time.sleep(2.5)
    r.d.key(CTRL_U); time.sleep(0.3); r.d.key(CTRL_U); time.sleep(1)
    r.dump('after-rewind', 1)
    r.cmd('/spk-list')
    r.cmd('/spk-go R2:W2')
    r.wait(lambda: r.running('R2', 'sh long.sh'), 90, 'R2 running')
    time.sleep(3)
    r.cmd('/clear')
    time.sleep(4)
    r.cmd('/spk-list')
    r.dump('after-clear', 1)
    r.wait(lambda: r.has('R1', 'R1.txt') and r.has('R2', 'R2.txt'), 240, 'both done after /clear')
    time.sleep(5)
    for n in ('R1', 'R2'):
        r.cmd(f'/spk-send {n} Use the Write tool to create the file afterclear.txt in your current directory containing exactly the word x. Then reply DONE.')
        r.wait(lambda: r.has(n, 'afterclear.txt'), 90, n + ' resumed after /clear')
    r.d.wait_quiet(3, 30)
    r.cmd('/spk-list')
    r.finish()
    r.summary()


def S6(root):
    """The mod reloaded mid-run (its file saved, then /reload-plugins): agents go on, timers restart, $.store keeps
    its values, commands stay. Then hooks that throw: a tool call, a prompt, a compaction."""
    r = Run(root, 'S6', env={'SPK_DROP_NOTES': '1', 'SPK_TICK': '1', 'SPK_THROW': '1', 'SPK_THROW_COMPACT': '1'}, allow=('Bash(echo THROWME)',))
    moddir = r.moddir
    r.start()
    r.cmd('/spk-store')
    r.cmd('/spk-go R1:W2')
    r.wait(lambda: r.running('R1', 'sh long.sh'), 90, 'R1 running')
    time.sleep(6)
    r.mark('save the mod file')
    with open(os.path.join(moddir, 'hooks', 'register.js'), 'a') as f:
        f.write('\n// saved again by the driver\n')
    time.sleep(12)
    r.cmd('/spk-store')
    r.cmd('/spk-list')
    r.dump('after-save', 1)
    r.cmd('/reload-plugins')
    time.sleep(10)
    r.cmd('/spk-store')
    r.dump('after-reload', 1)
    r.ask('Run this exact Bash command: echo THROWME. Then reply with what it printed.')
    r.ask('THROWPROMPT Reply with only the word through.')
    r.wait(lambda: r.has('R1', 'R1.txt'), 200, 'R1 done')
    n = r.stops()
    r.cmd('/compact')
    r.wait(lambda: r.ev('PostCompact') or r.modk('session.compact', lambda x: x['phase'] == 'after'), 120, 'compaction with a throwing hook')
    time.sleep(5)
    r.dump('end', 1)
    r.finish()
    r.summary()


def S7(root):
    """$.ui.ask while the lead's turn runs, while a permission prompt is open in the lead, and with nothing else open;
    each answered by typing 3, as baton's host does at a deadline."""
    r = Run(root, 'S7', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-ask 9000')
    r.cmd('Run this exact Bash command: sh slow.sh. Then reply with only the word slept.')
    r.wait(lambda: r.ev('PreToolUse', lambda x: x['input'].get('tool_name') == 'AskUserQuestion'), 40, 'ask shown during the lead turn')
    r.dump('ask-during-turn', 2)
    r.key(b'3', '3 for the question')
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 30, 'answer')
    r.wait(lambda: r.stops() >= 2, 60, 'lead turn over')
    r.d.wait_quiet(3, 20)
    r.cmd('/spk-ask 12000')
    r.cmd("Run this exact Bash command: python3 -c 'print(6*7)'. Then reply with what it printed.")
    r.wait(lambda: r.ev('PermissionRequest', lambda x: x['input'].get('tool_name') == 'Bash'), 60, 'permission prompt')
    r.wait(lambda: len(r.ev('PreToolUse', lambda x: x['input'].get('tool_name') == 'AskUserQuestion')) >= 2, 40, 'ask during the permission prompt')
    r.dump('ask-over-permission', 2)
    r.key(b'3', '3 with both open')
    r.dump('after-3', 2)
    time.sleep(3)
    r.dump('after-3b', 1)
    if len(r.modk('asked')) < 2:
        r.key(b'3', '3 again')
        r.dump('after-3-again', 2)
    r.wait(lambda: r.stops() >= 3, 60, 'lead turn over')
    r.d.wait_quiet(3, 20)
    r.cmd('/spk-ask 500')
    r.wait(lambda: len(r.ev('PreToolUse', lambda x: x['input'].get('tool_name') == 'AskUserQuestion')) >= 3, 30, 'ask alone')
    r.dump('ask-alone', 2)
    r.key(b'3', '3 alone')
    r.wait(lambda: len(r.modk('asked')) + len(r.modk('ask-failed')) >= 3, 30, 'answer')
    r.d.wait_quiet(3, 20)
    r.finish()
    r.summary()


def S8(root):
    """The human in the agent panel: opening a running agent's transcript and typing to it (what prompt.submit and the
    settings hooks see), then stopping it with x (what the mod sees, and whether a message reaches it after)."""
    r = Run(root, 'S8', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go T1:W2 T2:W2')
    r.wait(lambda: r.running('T1', 'sh long.sh') and r.running('T2', 'sh long.sh'), 90, 'running')
    time.sleep(3)
    r.dump('panel', 1)
    r.key(DOWN, 'down')
    r.dump('down1', 1)
    r.key(DOWN, 'down')
    r.dump('down2', 1)
    r.key(b'\r', 'enter on the row')
    r.dump('opened', 2)
    r.mark('type to the agent')
    r.d.type('Also use the Write tool to create the file typed.txt in your current directory containing exactly the word t.')
    time.sleep(0.5)
    r.d.enter()
    r.dump('typed', 3)
    r.key(ESC, 'esc back')
    r.dump('back', 1)
    r.wait(lambda: r.has('T1', 'typed.txt') or r.has('T2', 'typed.txt') or os.path.exists(os.path.join(r.repo, 'typed.txt')), 150, 'typed.txt')
    r.key(DOWN, 'down')
    r.key(DOWN, 'down')
    r.dump('select-for-x', 1)
    r.key(b'x', 'x on the row')
    r.dump('after-x', 2)
    time.sleep(5)
    r.cmd('/spk-list')
    stopped = [n for n in ('T1', 'T2') if any(x['reason'] == 'aborted' for x in r.turns(n))]
    r.mark(f'stopped by x: {stopped}')
    for n in stopped or ['T2']:
        r.cmd(f'/spk-send {n} Use the Write tool to create the file afterx.txt in your current directory containing exactly the word x. Then reply DONE.')
        time.sleep(10)
        r.cmd(f'/spk-msg {n} Use the Write tool to create the file afterx2.txt in your current directory containing exactly the word x. Then reply DONE.')
        time.sleep(15)
    r.wait(lambda: all(r.has(n, n + '.txt') for n in ('T1', 'T2') if n not in stopped), 200, 'the other done')
    r.cmd('/spk-list')
    r.d.wait_quiet(3, 30)
    r.dump('end', 1)
    r.finish()
    r.summary()


def S8b(root):
    """x on a running agent's row in the panel (focus in the panel, no transcript open, unlike S8): what the mod and
    the hooks see, and whether a message reaches it after."""
    r = Run(root, 'S8b', env={'SPK_DROP_NOTES': '1'})
    r.start()
    r.cmd('/spk-go X1:W2 X2:W2')
    r.wait(lambda: r.running('X1', 'sh long.sh') and r.running('X2', 'sh long.sh'), 90, 'running')
    time.sleep(3)
    r.key(DOWN, 'down')
    r.key(DOWN, 'down')
    r.dump('selected', 1)
    r.key(b'x', 'x on the row')
    r.dump('after-x', 2)
    r.key(ESC, 'esc: focus back to the prompt')
    r.dump('after-esc', 2)
    time.sleep(3)
    r.cmd('/spk-list')
    r.dump('listed', 1)
    stopped = [n for n in ('X1', 'X2') if any(x['reason'] == 'aborted' for x in r.turns(n))]
    r.mark(f'stopped by x: {stopped}')
    for n in stopped:
        r.cmd(f'/spk-send {n} Use the Write tool to create the file afterx.txt in your current directory containing exactly the word x. Then reply DONE.')
        time.sleep(12)
        r.cmd(f'/spk-msg {n} Use the Write tool to create the file afterx2.txt in your current directory containing exactly the word x. Then reply DONE.')
        time.sleep(15)
    r.wait(lambda: all(r.has(n, n + '.txt') for n in ('X1', 'X2') if n not in stopped), 200, 'the other done')
    r.cmd('/spk-list')
    r.d.wait_quiet(3, 30)
    r.dump('end', 1)
    r.finish()
    r.summary()


def mode(r, **m):
    json.dump(m, open(os.path.join(r.work, 'proxy.mode'), 'w'))
    r.mark('proxy mode ' + json.dumps(m))


def S9(root, port=18433):
    """API errors in a background agent, injected by proxy.py: a bad request (400), overloads (529) that pass, and a
    usage limit (429 with the unified rate-limit headers). Its status, turn.complete reason and hooks, and a message
    after."""
    work = os.path.join(root, 'S9')
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    px = subprocess.Popen([sys.executable, os.path.join(HERE, 'proxy.py'), str(port), work])
    time.sleep(1)
    try:
        r = Run(root, 'S9', env={'SPK_DROP_NOTES': '1', 'ANTHROPIC_BASE_URL': f'http://127.0.0.1:{port}'})
        mode(r, status=400, count=0)
        r.start()
        for name, m in (('E1', dict(status=400, count=1)), ('E2', dict(status=529, count=3)), ('E3', dict(status=429, count=200, retry_after=30, reset_in=600))):
            mode(r, **m)
            r.cmd(f'/spk-go {name}:ERR')
            r.wait(lambda: r.turns(name) or r.ev('StopFailure', lambda x: x['input'].get('agent_id') == r.agent(name)), 240 if name != 'E3' else 180, name + ' turn over')
            r.dump(name + '-over', 2)
            r.cmd('/spk-list')
            mode(r, status=400, count=0)
            time.sleep(3)
            if not r.has(name, name + '.txt'):
                r.cmd(f'/spk-send {name} Go on with your task.')
                r.wait(lambda: r.has(name, name + '.txt'), 120, name + ' resumed after the error')
            r.d.wait_quiet(3, 30)
        r.cmd('/spk-list')
        r.finish()
        r.summary()
    finally:
        px.kill()


def rss(pid):
    """RSS in MB of pid and its descendants."""
    out = subprocess.run(['ps', '-A', '-o', 'pid=,ppid=,rss='], capture_output=True, text=True).stdout.split('\n')
    kids, mem = {}, {}
    for l in out:
        p = l.split()
        if len(p) == 3:
            kids.setdefault(int(p[1]), []).append(int(p[0]))
            mem[int(p[0])] = int(p[2])
    tot, todo, own = 0, [pid], mem.get(pid, 0)
    while todo:
        p = todo.pop()
        tot += mem.get(p, 0)
        todo += kids.get(p, [])
    return round(own / 1024), round(tot / 1024)


def S10(root, minutes=20):
    """Four phase agents kept at work for 20 minutes, each sent its next round as it goes idle: memory and stability."""
    r = Run(root, 'S10', env={'SPK_DROP_NOTES': '1'})
    r.start()
    samples = [(0, *rss(r.d.pid))]
    r.cmd('/spk-go K1:S K2:S K3:S K4:S')
    r.wait(lambda: all(r.agent(n) for n in ('K1', 'K2', 'K3', 'K4')), 120, 'four spawned')
    r.cmd(f'/spk-soak {minutes} K1 K2 K3 K4')
    t0 = time.time()
    while time.time() - t0 < minutes * 60 + 90 and r.d.alive:
        time.sleep(30)
        samples.append((round(time.time() - t0), *rss(r.d.pid)))
        r.d.note(f'rss {samples[-1]}')
    r.dump('end', 1)
    r.cmd('/spk-list')
    rounds = {n: len(r.turns(n)) for n in ('K1', 'K2', 'K3', 'K4')}
    r.finish()
    r.summary({'rss_mb': samples, 'turns': rounds})


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
