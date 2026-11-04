#!/usr/bin/env python3
"""Spike 43: the model and effort of a session and of its agents (Claude Code 2.1.318, macOS).

Usage: run.py <workroot>. Spike 40's harness without pyte: claude in a PTY in a fresh git repo, no user settings
(--setting-sources project,local), acceptEdits with an exact allow rule, the mod (mod/, copied per run; it registers
agent types with their own effort and model, spawns and messages agents as baton's mod does, and drops agents' reports
to the lead) and hook.py on the settings events and as the status line, logging to hooks.jsonl; the mod logs to
mod.jsonl.*. SPK_CLAUDE names the claude binary. Three sessions of one conversation:

  A  `--model haiku --effort low`. A lead turn; five agents: A1 of a type with effort max; A2 of a type with effort
     low, spawned with model sonnet; A3 of a type with neither; A4 of a type with model sonnet and effort medium,
     spawned with model haiku; A5 of that type with no model. Then t-max registered again at low and A1 resumed by a
     message, t-none registered again at high and A3 resumed by a message, and B1 spawned of the re-registered t-max.
  B  `--resume <id> --model sonnet --effort high`: the screen at the resume, a lead turn.
  C  `--resume <id>` with neither flag: a lead turn.

Read from the transcripts (the lead's and each agent's), the hook inputs, the status line inputs, the mod's log, and
~/.claude/settings.json before and after.
"""
import glob, json, os, shlex, shutil, subprocess, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver

HOOK = os.path.join(HERE, 'hook.py')
MOD = os.path.join(HERE, 'mod')
CLAUDE = os.environ.get('SPK_CLAUDE') or 'claude'
EVENTS = ['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop', 'StopFailure',
          'SubagentStart', 'SubagentStop']
ALLOW = ('Bash(pwd)',)
USER_SETTINGS = os.path.expanduser('~/.claude/settings.json')
DIALOG = ('Esctocancel', 'Entertoconfirm', 'Doyouwant', '❯1.')


def git(cwd, *a):
    return subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=cwd, capture_output=True, text=True)


def recs(path):
    try:
        return [json.loads(l) for l in open(path) if l.strip()]
    except FileNotFoundError:
        return []


def user_tier():
    try:
        s = json.load(open(USER_SETTINGS))
    except Exception as e:
        return {'error': str(e)}
    return {'model': s.get('model'), 'effortLevel': s.get('effortLevel'), 'modelSettings': s.get('modelSettings')}


class Run:
    def __init__(self, root):
        self.work = os.path.join(root, '43')
        shutil.rmtree(self.work, ignore_errors=True)
        self.repo = os.path.join(self.work, 'repo')
        os.makedirs(self.repo)
        git(self.repo, 'init', '-q', '-b', 'main')
        open(os.path.join(self.repo, 'README'), 'w').write('spike 43\n')
        open(os.path.join(self.repo, '.gitignore'), 'w').write('screen.*\n')
        git(self.repo, 'add', '.')
        git(self.repo, 'commit', '-qm', 'init')
        self.mod = os.path.join(self.work, 'mod')
        shutil.copytree(MOD, self.mod)
        self.hooks = os.path.join(self.work, 'hooks.jsonl')
        self.modlog = os.path.join(self.work, 'mod.jsonl')
        log = shlex.quote(self.hooks)
        hk = lambda ev: f'python3 {shlex.quote(HOOK)} {ev} {log}'
        self.settings = json.dumps({
            'hooks': {ev: [{'hooks': [{'type': 'command', 'command': hk(ev)}]}] for ev in EVENTS},
            'statusLine': {'type': 'command', 'command': hk('StatusLine')},
        })
        self.scen = '43-' + str(int(time.time()))
        self.version = subprocess.run([CLAUDE, '--version'], capture_output=True, text=True).stdout.strip()
        self.notes = {'user_settings_before': user_tier()}
        self.d = None
        self.label = None

    # the session

    def open(self, label, flags):
        self.label = label
        argv = [CLAUDE, *flags, '--setting-sources', 'project,local', '--permission-mode', 'acceptEdits',
                '--plugin-dir', self.mod, '--settings', self.settings]
        for a in ALLOW:
            argv += ['--allowedTools', a]
        self.mark(f'open {label}: {" ".join(flags)}')
        self.d = Driver(self.repo, argv, env_extra={'SPK_LOG': self.modlog, 'SPK_SCEN': self.scen})

    def close(self):
        self.cmd('/exit')
        time.sleep(4)
        self.d.close()
        time.sleep(1)
        shutil.move(os.path.join(self.repo, 'screen.log'), os.path.join(self.work, f'screen-{self.label}.log'))

    def mark(self, label):
        with open(self.hooks, 'a') as f:
            f.write(json.dumps({'t': f'{time.time():.3f}', 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'input': {'step': label}}) + '\n')
        print(f'[{time.strftime("%H:%M:%S")}] {label}', flush=True)

    def tail(self, n=3000):
        with self.d.lock:
            return self.d.tail[-n:].decode('utf-8', 'replace')

    def dialog(self):
        t = self.tail(1500).replace(' ', '')
        return [w for w in DIALOG if w in t]

    def cmd(self, text):
        self.mark('type: ' + text[:120])
        self.d.type(text)
        time.sleep(0.6)
        self.d.enter()
        time.sleep(1.5)

    def ev(self, name, pred=lambda r: True):
        return [r for r in recs(self.hooks) if r['ev'] == name and pred(r)]

    def stops(self):
        return len(self.ev('Stop', lambda x: not x['input'].get('agent_id')))

    def ask(self, text, timeout=120):
        n = self.stops()
        self.cmd(text)
        ok = self.d.wait(lambda: self.stops() > n, timeout, 'lead stop')
        self.d.wait_quiet(2, 20)
        return ok

    def modrecs(self):
        out = []
        for p in sorted(glob.glob(self.modlog + '.*')):
            out += recs(p)
        return sorted(out, key=lambda r: r['t'])

    def modk(self, kind, pred=lambda r: True):
        return [r for r in self.modrecs() if r['kind'] == kind and pred(r)]

    def agent(self, name):
        s = self.modk('spawned', lambda x: x['name'] == name)
        return s[-1]['r'].get('agentId') if s else None

    def turns(self, name):
        aid = self.agent(name)
        return [x for x in self.modk('turn.complete') if aid and x.get('agentId') == aid]

    def wait(self, cond, timeout, what):
        return self.d.wait(cond, timeout, what)

    def start(self, trust=True):
        if trust:
            self.d.trust(25)
        self.d.wait(lambda: self.ev('SessionStart', lambda x: x['t'] > self.opened_at()), 40, 'SessionStart')
        self.d.wait_quiet(3, 40)

    def opened_at(self):
        m = [r for r in recs(self.hooks) if r['ev'] == 'DRIVER' and r['input']['step'].startswith('open ' + self.label)]
        return m[-1]['t'] if m else '0'


def assistant_entries(path):
    out = []
    for line in open(path, errors='replace'):
        try:
            d = json.loads(line)
        except Exception:
            continue
        if d.get('type') != 'assistant':
            continue
        m = d.get('message') or {}
        out.append({'ts': d.get('timestamp'), 'model': m.get('model'), 'requestedModel': d.get('requestedModel'),
                    'effort': d.get('effort'), 'perTurnEffort': d.get('perTurnEffort'), 'version': d.get('version'),
                    'text': next((c.get('text', '')[:40] for c in (m.get('content') or []) if isinstance(c, dict) and c.get('type') == 'text'), None)})
    return out


def runs(entries):
    """Consecutive entries with the same model and effort, folded: [(model, effort, count, first ts)]."""
    out = []
    for e in entries:
        k = (e['model'], e['requestedModel'], e['effort'])
        if out and out[-1][0] == k:
            out[-1][1] += 1
        else:
            out.append([k, 1, e['ts']])
    return [{'model': k[0], 'requested': k[1], 'effort': k[2], 'entries': n, 'from': ts} for k, n, ts in out]


def main(root):
    r = Run(root)
    print('claude', r.version, 'work', r.work, flush=True)

    # A
    r.open('A', ['--model', 'haiku', '--effort', 'low'])
    r.start()
    r.wait(lambda: len(r.modk('registered')) + len(r.modk('register-failed')) >= 4, 30, 'types registered')
    r.ask('Reply with only the word READY.')
    r.cmd('/spk-go A1:t-max A2:t-low:sonnet A3:t-none A4:t-sonnet:haiku A5:t-sonnet')
    for name in ('A1', 'A2', 'A3', 'A4', 'A5'):
        r.wait(lambda: r.turns(name), 240, f'{name} done')
    time.sleep(3)
    r.mark('resume step')
    r.cmd('/spk-re t-max low')
    r.wait(lambda: r.modk('registered', lambda x: x['why'] == 'again') or r.modk('register-failed', lambda x: x['why'] == 'again'), 20, 't-max again')
    r.cmd('/spk-msg A1 Run this exact Bash command: pwd. Then reply with only the word AGAIN.')
    r.wait(lambda: len(r.turns('A1')) >= 2, 120, 'A1 resumed and done')
    r.cmd('/spk-re t-none high')
    r.cmd('/spk-msg A3 Run this exact Bash command: pwd. Then reply with only the word AGAIN.')
    r.wait(lambda: len(r.turns('A3')) >= 2, 120, 'A3 resumed and done')
    r.cmd('/spk-go B1:t-max')
    r.wait(lambda: r.turns('B1'), 120, 'B1 done')
    r.cmd('/spk-model')
    time.sleep(2)
    r.notes['dialog_A'] = r.dialog()
    r.close()
    r.notes['user_settings_after_A'] = user_tier()

    starts = r.ev('SessionStart')
    sid = starts[0]['input'].get('session_id')
    tp = starts[0]['input'].get('transcript_path')

    # B
    r.open('B', ['--resume', sid, '--model', 'sonnet', '--effort', 'high'])
    r.start(trust=False)
    time.sleep(4)
    r.notes['dialog_B_at_resume'] = r.dialog()
    r.notes['screen_B_at_resume'] = r.tail(1500)
    if not r.notes['dialog_B_at_resume']:
        r.ask('Reply with only the word OK2.')
    r.cmd('/spk-model')
    time.sleep(2)
    r.close()
    r.notes['user_settings_after_B'] = user_tier()

    # C
    r.open('C', ['--resume', sid])
    r.start(trust=False)
    time.sleep(4)
    r.notes['dialog_C_at_resume'] = r.dialog()
    if not r.notes['dialog_C_at_resume']:
        r.ask('Reply with only the word OK3.')
    r.close()
    r.notes['user_settings_after_C'] = user_tier()

    # what each request carried
    out = {'claude': r.version, 'session': sid, 'transcript': tp, 'notes': r.notes}
    lead_tps = sorted({x['input'].get('transcript_path') for x in r.ev('SessionStart') if x['input'].get('transcript_path')})
    out['lead'] = {p: runs(assistant_entries(p)) for p in lead_tps if os.path.exists(p)}
    ids = {}
    for s in r.modk('spawned'):
        if s['r'].get('agentId'):
            ids[s['r']['agentId']] = s['name']
    out['agents'] = {}
    for p in sorted(glob.glob(os.path.join(os.path.dirname(tp), '*', 'subagents', 'agent-*.jsonl'))):
        aid = os.path.basename(p)[len('agent-'):-len('.jsonl')]
        if aid in ids:
            out['agents'][ids[aid]] = {'id': aid, 'runs': runs(assistant_entries(p))}
    out['spawned'] = [{'name': s['name'], 'type': s['type'], 'model_asked': s['model'], 'r': s['r']} for s in r.modk('spawned')]
    out['spawn_failed'] = r.modk('spawn-failed')
    out['registered'] = [{'name': x['name'], 'def': x['def'], 'why': x['why'], 'r': x.get('r')} for x in r.modk('registered')]
    out['register_failed'] = r.modk('register-failed')
    out['session_model'] = [x['model'] for x in r.modk('session.model')]
    out['sent'] = [x for x in r.modk('sent') + r.modk('send-failed')]
    seen, hooks = set(), []
    for x in recs(r.hooks):
        i = x['input']
        if x['ev'] == 'DRIVER':
            hooks.append({'step': i['step']})
            continue
        if x['ev'] == 'StatusLine':
            k = ('StatusLine', (i.get('model') or {}).get('id'), json.dumps(i.get('effort')), json.dumps(i.get('thinking')))
        else:
            k = (x['ev'], ids.get(i.get('agent_id'), i.get('agent_id')), i.get('agent_type'), json.dumps(i.get('effort')), i.get('model'), i.get('source'))
        if k in seen:
            continue
        seen.add(k)
        hooks.append({'hms': x['hms'], 'ev': x['ev'], 'key': k[1:]})
    out['hooks'] = hooks
    path = os.path.join(r.work, 'results.json')
    json.dump(out, open(path, 'w'), indent=1)
    print(json.dumps({k: out[k] for k in ('lead', 'agents', 'spawned', 'register_failed', 'session_model', 'notes')}, indent=1))
    print('results:', path)


if __name__ == '__main__':
    main(sys.argv[1])
