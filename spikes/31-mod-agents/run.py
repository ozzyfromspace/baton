#!/usr/bin/env python3
"""Spike 31: phase agents started and steered by a mod (Claude Code 2.1.296, Haiku). Usage: run.py <scenario> <workroot>

The mod (mod/hooks/register.js) logs everything it sees to mod.jsonl; hook.py logs every settings hook event
to hooks.jsonl. Each step's screen is rendered to screen-<label>.txt (via ../26-bg-sessions/vt.py).
"""
import json, os, shlex, shutil, signal, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver
import pyte  # the spike runs under a venv that has it: a full VT emulator renders Claude Code's screen as drawn

HOOK = os.path.join(HERE, 'hook.py')
MOD = os.path.join(HERE, 'mod')
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure',
          'PermissionRequest', 'Notification', 'Stop', 'StopFailure', 'PreCompact', 'PostCompact',
          'SubagentStart', 'SubagentStop', 'WorktreeCreate', 'WorktreeRemove', 'PermissionDenied']
ROWS, COLS = 50, 160


def git(cwd, *a):
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def setup(root, name):
    work = os.path.join(root, name)
    shutil.rmtree(work, ignore_errors=True)
    repo = os.path.join(work, 'repo')
    os.makedirs(repo)
    git(repo, 'init', '-q', '-b', 'main')
    open(os.path.join(repo, 'README'), 'w').write('spike\n')
    open(os.path.join(repo, 'slow.sh'), 'w').write('sleep 25\necho slept\n')
    words = 'alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa'.split()
    for i in range(1, 5):  # about 25k tokens each: four of them push a small compaction window over
        open(os.path.join(repo, f'big{i}.txt'), 'w').write('\n'.join(' '.join(words[(j + k) % 16] for k in range(12)) + f' {i}.{j}' for j in range(1500)) + '\n')
    git(repo, 'add', '.')
    git(repo, 'commit', '-qm', 'init')
    wts = {}
    for p in ('P1', 'P2'):
        wt = os.path.join(work, 'wt-' + p)
        git(repo, 'worktree', 'add', '-q', '-b', 'phase/' + p, wt)
        wts[p] = wt
    hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {shlex.quote(os.path.join(work, "hooks.jsonl"))}'}]}] for ev in EVENTS}
    return work, repo, wts, json.dumps({'hooks': hooks})


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


class Run:
    def __init__(self, root, name, extra_args=(), env=None, add_dirs=('P1', 'P2'), resume=None, reuse=False):
        if reuse:
            self.work = os.path.join(root, name)
            self.repo = os.path.join(self.work, 'repo')
            self.wts = {p: os.path.join(self.work, 'wt-' + p) for p in ('P1', 'P2')}
            hooks = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {shlex.quote(os.path.join(self.work, "hooks.jsonl"))}'}]}] for ev in EVENTS}
            settings = json.dumps({'hooks': hooks})
        else:
            self.work, self.repo, self.wts, settings = setup(root, name)
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.mod = os.path.join(self.work, 'mod.jsonl')
        argv = ['claude', '--model', 'haiku', '--permission-mode', 'acceptEdits', '--plugin-dir', MOD, '--settings', settings]
        for p in add_dirs:
            argv += ['--add-dir', self.wts[p]]
        if resume:
            argv += ['--resume', resume]
        argv += list(extra_args)
        argv += ['--allowedTools', 'Bash(pwd)', '--allowedTools', 'Bash(sh slow.sh)']
        e = {'SPK_LOG': self.mod, 'SPK_WT1': self.wts['P1'], 'SPK_WT2': self.wts['P2']}
        e.update(env or {})
        self.d = Driver(self.repo, argv, env_extra=e, rows=ROWS, cols=COLS)
        self.n = 0

    def dump(self, label, settle=1.0):
        time.sleep(settle)
        with self.d.lock:
            raw = open(self.d.screen_path, 'rb').read()
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

    def ev(self, name, pred=lambda r: True):
        return [r for r in recs(self.hooks) if r['ev'] == name and pred(r)]

    def modk(self, kind, pred=lambda r: True):
        return [r for r in recs(self.mod) if r['kind'] == kind and pred(r)]

    def cmd(self, text):
        self.mark('type: ' + text)
        self.d.type(text)
        time.sleep(0.4)
        self.d.enter()
        time.sleep(1.5)

    def ask(self, text, timeout=120):
        n = len(self.ev('Stop')) + len(self.ev('StopFailure'))
        self.cmd(text)
        ok = self.d.wait(lambda: len(self.ev('Stop')) + len(self.ev('StopFailure')) > n, timeout, 'lead stop')
        self.d.wait_quiet(2, 20)
        return ok

    def wait(self, cond, timeout, what):
        return self.d.wait(cond, timeout, what)

    def finish(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()

    def summary(self):
        out = {'files': {}}
        bwts = [os.path.join(self.work, d) for d in sorted(os.listdir(self.work)) if d.startswith('bwt-')]
        for where in [self.repo] + list(self.wts.values()) + bwts:
            for f in sorted(os.listdir(where)):
                if f.endswith('.txt') and not f.startswith('big'):
                    out['files'][os.path.relpath(os.path.join(where, f), self.work)] = open(os.path.join(where, f)).read().strip()
        sid = sorted({r['input'].get('session_id') for r in recs(self.hooks) if r['input'].get('session_id')})
        out['sessions'] = sid
        out['hook_events_by_agent'] = {}
        for r in recs(self.hooks):
            if r['ev'] == 'DRIVER':
                continue
            k = r['input'].get('agent_type', '') + '/' + (r['input'].get('agent_id') or 'main')[:10]
            out['hook_events_by_agent'].setdefault(k, []).append(r['ev'] + (':' + r['input'].get('tool_name') if r['input'].get('tool_name') else ''))
        for k, v in out['hook_events_by_agent'].items():
            out['hook_events_by_agent'][k] = ' '.join(v)
        tps = sorted({r['input'].get('transcript_path') for r in recs(self.hooks) if r['input'].get('transcript_path')})
        for tp in tps:
            if os.path.exists(tp):
                shutil.copy(tp, os.path.join(self.work, 'transcript-' + os.path.basename(tp)))
                sub = os.path.join(tp[:-len('.jsonl')], 'subagents')
                if os.path.isdir(sub):
                    shutil.copytree(sub, os.path.join(self.work, 'subagents'), dirs_exist_ok=True)
        with open(os.path.join(self.work, 'summary.json'), 'w') as f:
            json.dump(out, f, indent=1)
        print(json.dumps(out, indent=1))
        return out


def agent_of(r, name):
    s = r.modk('spawned', lambda x: x['name'] == name)
    return s[-1]['r'].get('agentId') if s else None


def done_turns(r, name):
    aid = agent_of(r, name)
    return [x for x in r.modk('turn.complete') if aid and x.get('agentId') == aid]


def A(root):
    """Two phase agents from the mod, each in its own worktree; hooks, context, a blocked stop, the panel; the lead
    compacted while P2 runs, P1 resumed by a message, the lead woken by a prompt."""
    r = Run(root, 'A', env={'SPK_BLOCK_STOP': '1'})
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.dump('start')
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-go')
    r.wait(lambda: len(r.modk('spawned')) >= 2 or r.modk('spawn-failed'), 30, 'spawned')
    r.dump('spawned-2s', 2)
    r.dump('spawned-8s', 6)
    r.cmd('/spk-compact')
    r.wait(lambda: r.modk('compacted') or r.modk('compact-failed') or r.ev('SessionStart', lambda x: x['input'].get('source') == 'compact'), 120, 'compaction')
    r.dump('after-compact', 2)
    r.wait(lambda: os.path.exists(os.path.join(r.wts['P2'], 'stop.txt')) and os.path.exists(os.path.join(r.wts['P1'], 'stop.txt')), 180, 'both stop.txt')
    r.wait(lambda: len(r.ev('SubagentStop')) >= 4, 60, 'second stops')
    r.dump('agents-done', 3)
    r.cmd('/spk-list')
    r.dump('list', 2)
    r.cmd("/spk-send P1 Use the Write tool to create the file resumed.txt in your current directory containing exactly the word again. Then reply DONE.")
    r.wait(lambda: os.path.exists(os.path.join(r.wts['P1'], 'resumed.txt')), 120, 'resumed.txt')
    r.dump('resumed', 3)
    n = len(r.ev('Stop'))
    r.cmd('/spk-submit Reply with only the word woken.')
    r.wait(lambda: len(r.ev('Stop')) > n, 90, 'lead woken')
    r.dump('woken', 2)
    r.finish()
    r.summary()


def A3(root):
    """As A2, with the phase agents spawned as spk:phase, an agent type the mod registers with isolation worktree."""
    A2(root, 'A3', '/spk-go3')


def A2(root, name='A2', go='/spk-go2'):
    """Phase agents through the Agent tool with isolation worktree, from the mod; baton's WorktreeCreate hook makes
    the worktrees. The lead compacted (from a mod timer) while P2 runs; P1 resumed by a message; the lead woken."""
    r = Run(root, name, env={'SPK_BLOCK_STOP': '1'}, add_dirs=())
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd(go)
    if not r.wait(lambda: len(r.modk('list', lambda x: len(x['list']) >= 2)), 40, 'two agents listed'):
        r.dump('no-agents', 1)
        r.finish()
        r.summary()
        return
    r.dump('spawned-3s', 3)
    r.cmd('/spk-compact')
    r.wait(lambda: r.modk('compacted') or r.modk('compact-failed'), 120, 'compaction')
    r.dump('after-compact', 2)
    r.wait(lambda: len(r.ev('SubagentStop')) >= 4, 240, 'four subagent stops')
    r.d.wait_quiet(3, 30)
    r.dump('agents-done', 2)
    r.cmd('/spk-list')
    r.cmd("/spk-send P1 Use the Write tool to create the file resumed.txt in your current directory containing exactly the word again. Then reply DONE.")
    r.wait(lambda: any(os.path.exists(os.path.join(r.work, d, 'resumed.txt')) for d in os.listdir(r.work)) or os.path.exists(os.path.join(r.repo, 'resumed.txt')), 120, 'resumed.txt')
    r.dump('resumed', 3)
    n = len(r.ev('Stop'))
    r.cmd('/spk-submit Reply with only the word woken.')
    r.wait(lambda: len(r.ev('Stop')) > n, 90, 'lead woken')
    r.dump('woken', 2)
    r.finish()
    r.summary()
    print(subprocess.run(['git', 'worktree', 'list'], cwd=r.repo, capture_output=True, text=True).stdout)
    print(subprocess.run(['git', 'branch', '-a'], cwd=r.repo, capture_output=True, text=True).stdout)


def B(root):
    """A phase agent that needs permission (Bash date): where the prompt shows and what the hook sees. Then its
    report after a resume, dropped before the lead's model reads it; and the lead woken from a mod timer."""
    r = Run(root, 'B', env={'SPK_DROP_NOTES': '1'}, add_dirs=())
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-go3 P3')
    r.wait(lambda: r.ev('PermissionRequest'), 60, 'permission request')
    r.dump('perm', 2)
    r.mark('enter: approve once')
    r.d.key(b'\r')
    r.wait(lambda: done_turns(r, 'P3'), 60, 'P3 done')
    r.d.wait_quiet(3, 20)
    r.dump('p3-done', 1)
    r.cmd("/spk-send P3 Use the Write tool to create the file again.txt in your current directory containing exactly the word again. Then reply DONE.")
    r.wait(lambda: len(done_turns(r, 'P3')) >= 2, 90, 'P3 resumed and done')
    time.sleep(8)
    r.dump('after-resume', 1)
    n = len(r.ev('Stop'))
    r.cmd('/spk-submit Reply with only the word woken.')
    r.wait(lambda: len(r.ev('Stop')) > n, 90, 'lead woken')
    r.dump('woken', 2)
    r.finish()
    r.summary()


def C(root):
    """claude killed (SIGKILL) while a phase agent runs; the session resumed with --resume: is the agent listed, and
    does a message to its id resume it, in its worktree?"""
    r = Run(root, 'C', env={'SPK_DROP_NOTES': '1'}, add_dirs=())
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-go3 P2')
    r.wait(lambda: r.modk('tool.call', lambda x: x.get('command') == 'sh slow.sh'), 60, 'P2 running slow.sh')
    time.sleep(3)
    r.dump('before-kill', 1)
    aid = agent_of(r, 'P2')
    sid = r.ev('SessionStart')[0]['input']['session_id']
    r.mark(f'kill -9 {r.d.pid} (agent {aid}, session {sid})')
    os.kill(r.d.pid, signal.SIGKILL)
    time.sleep(3)
    r.d.close()
    r2 = Run(root, 'C', env={'SPK_DROP_NOTES': '1', 'SPK_LOG': os.path.join(r.work, 'mod2.jsonl')}, add_dirs=(), resume=sid, reuse=True)
    r2.d.trust(15)
    r2.d.wait_quiet(3, 40)
    r2.dump('resumed', 1)
    r2.cmd('/spk-list')
    time.sleep(3)
    r2.cmd(f'/spk-send {aid} Use the Write tool to create the file p2b.txt in your current directory containing exactly the word back. Then reply DONE.')
    wt = os.path.join(r.work, 'bwt-agent-' + aid)
    r2.wait(lambda: os.path.exists(os.path.join(wt, 'p2b.txt')) or os.path.exists(os.path.join(r.repo, 'p2b.txt')), 120, 'p2b.txt')
    r2.d.wait_quiet(3, 30)
    r2.dump('after-send', 1)
    r2.finish()
    r2.summary()


def D1(root):
    """A phase agent's permission prompt (python3 is not allowed): where it shows, what the hook and the mod see; then
    a question the mod puts to the human with $.ui.ask, answered with Enter."""
    r = Run(root, 'D1', env={'SPK_DROP_NOTES': '1'}, add_dirs=())
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-go3 P4')
    r.wait(lambda: r.ev('PermissionRequest'), 90, 'permission request')
    time.sleep(3)
    r.dump('perm', 1)
    r.mark('enter: approve once')
    r.d.key(b'\r')
    r.wait(lambda: done_turns(r, 'P4'), 90, 'P4 done')
    r.d.wait_quiet(3, 20)
    r.cmd('/spk-ask')
    r.wait(lambda: r.ev('PreToolUse', lambda x: x['input'].get('tool_name') == 'AskUserQuestion') or r.ev('Notification', lambda x: 'question' in json.dumps(x['input']).lower()), 30, 'question shown')
    time.sleep(3)
    r.dump('ask', 1)
    r.mark('enter: first option')
    r.d.key(b'\r')
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 30, 'answer')
    r.dump('asked', 2)
    r.finish()
    r.summary()


def D2(root):
    """A phase agent that fills a small context window (CLAUDE_AUTOCOMPACT_PCT_OVERRIDE): does it compact by itself,
    which hooks fire, and does it still know what SubagentStart gave it?"""
    r = Run(root, 'D2', env={'SPK_DROP_NOTES': '1', 'CLAUDE_AUTOCOMPACT_PCT_OVERRIDE': '25'}, add_dirs=())
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-go3 P5')
    r.wait(lambda: done_turns(r, 'P5'), 420, 'P5 done')
    r.d.wait_quiet(3, 30)
    r.dump('p5-done', 1)
    r.finish()
    r.summary()


WORKFLOW = r"""export const meta = {
  name: 'spk-plan',
  description: 'Spike: four phases, P1 and P2 at once, P3 after P1, P4 in a worktree',
  phases: [{ title: 'Step 1' }, { title: 'Step 2' }],
}
const p1 = agent('You are working on phase P1 of a plan. Run the Bash command `pwd`. Then use the Write tool to create the file wf-p1.txt in the current directory containing exactly the word one. Then reply with the codeword you were given, if any, and the word DONE.', { label: 'P1 — first', phase: 'Step 1' })
const p2 = agent('You are working on phase P2 of a plan. Run this exact Bash command: sleep 25 && echo slept. Then use the Write tool to create the file wf-p2.txt in the current directory containing exactly the word two. Then reply DONE.', { label: 'P2 — second', phase: 'Step 1' })
const p4 = agent('You are working on phase P4 of a plan. Run the Bash command `pwd`. Then use the Write tool to create the file wf-p4.txt in the current directory containing exactly the word four. Then reply with the output of pwd and the word DONE.', { label: 'P4 — isolated', phase: 'Step 1', isolation: 'worktree' })
const p3 = p1.then(() => agent('You are working on phase P3 of a plan. Use the Write tool to create the file wf-p3.txt in the current directory containing exactly the word three. Then reply DONE.', { label: 'P3 — after P1', phase: 'Step 2' }))
const all = await Promise.all([p1, p2, p3, p4])
return { p1: all[0], p2: all[1], p3: all[2], p4: all[3] }
"""


def put_workflow(r):
    d = os.path.join(r.repo, '.claude', 'workflows')
    os.makedirs(d, exist_ok=True)
    open(os.path.join(d, 'spk-plan.js'), 'w').write(WORKFLOW)


def wf_files(r):
    return [f for f in ('wf-p1.txt', 'wf-p2.txt', 'wf-p3.txt') if os.path.exists(os.path.join(r.repo, f))]


def watch_workflow(r, label):
    r.dump(label + '-3s', 3)
    r.dump(label + '-10s', 7)
    r.cmd('/workflows')
    r.dump(label + '-view', 2)
    r.d.key(b'\r')
    r.dump(label + '-view-phase', 2)
    r.d.key(b'\r')
    r.dump(label + '-view-agent', 2)
    for _ in range(4):
        r.d.key(b'\x1b')
        time.sleep(0.6)
    r.dump(label + '-view-closed', 1)
    r.wait(lambda: len(wf_files(r)) == 3, 240, 'workflow files')
    r.wait(lambda: r.modk('workflow-called') or r.modk('workflow-failed') or r.ev('PostToolUse', lambda x: x['input'].get('tool_name') == 'Workflow' and 'agent_id' not in x['input']), 30, 'workflow returned')
    r.d.wait_quiet(5, 120)
    r.dump(label + '-done', 2)


def W1(root):
    """A saved workflow started by the mod ($.tool.call Workflow), approved by a PreToolUse hook: hooks inside
    workflow agents, the progress view, a worktree-isolated agent."""
    r = Run(root, 'W1', env={'SPK_ALLOW_WORKFLOW': '1', 'SPK_BLOCK_STOP': '1'})
    put_workflow(r)
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-wf spk-plan')
    watch_workflow(r, 'W1')
    r.finish()
    r.summary()
    print(subprocess.run(['git', 'worktree', 'list'], cwd=r.repo, capture_output=True, text=True).stdout)


def W2(root):
    """The same workflow started by typing its command, /spk-plan: does a typed workflow command start without
    the model choosing to, and what does the lead's turn look like?"""
    r = Run(root, 'W2', env={'SPK_ALLOW_WORKFLOW': '1'})
    put_workflow(r)
    r.d.trust(25)
    r.d.wait_quiet(3, 40)
    r.ask('Reply with only the word ready.')
    r.cmd('/spk-plan')
    watch_workflow(r, 'W2')
    r.finish()
    r.summary()


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
