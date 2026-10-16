#!/usr/bin/env python3
"""Spike 38, hosted: baton moves the approved plan out of Claude Code's way, and the boundary compaction attaches none.

Usage: hosted.py <workroot> <baton binary>. Runs a dev build of baton hosting claude (Haiku) in a PTY, with this
repository's plugin (--plugin-dir, baton's mod with it) and a scratch BATON_HOME, in a fresh git repo whose
.claude/settings.json sets plansDirectory to "plans". Plays the human as the e2e harness does: plan mode, a
two-phase plan, and Enter on every dialog baton logs (the plan's approval, the model's permission prompts). Stops once
P0 has started from the boundary's brief (or after 8 minutes), and prints what happened: baton's events, the plan
record, the plans folder, and per compact boundary in the lead's transcript the attachments after it.
"""
import glob, json, os, shutil, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver  # noqa: E402

REPO_ROOT = os.path.abspath(os.path.join(HERE, '..', '..'))
work, baton = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2])
repo = os.path.join(work, 'repo')
PLAN = ('Plan this tiny task. Write the plan to your plan file with exactly these two headings and nothing else as '
        'headings, under the title "# Spike thirty-eight": "## P0 — Say hello" (step: run echo hello) and '
        '"## P1 — Say goodbye" (step: run echo goodbye). Do not explore the repository. Then call ExitPlanMode.')


def git(*a):
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=repo, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def setup():
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(os.path.join(repo, '.claude'))
    for d in ('home', 'gnupg'):
        os.makedirs(os.path.join(work, d), mode=0o700)
    git('init', '-q', '-b', 'main')
    open(os.path.join(repo, 'README'), 'w').write('spike 38, hosted\n')
    open(os.path.join(repo, '.gitignore'), 'w').write('screen.log\nplans/\n')
    json.dump({'plansDirectory': 'plans'}, open(os.path.join(repo, '.claude', 'settings.json'), 'w'))
    git('add', '.')
    git('commit', '-qm', 'init')


def events():
    out = []
    for path in glob.glob(os.path.join(repo, '.baton', 'runs', '*', 'events.jsonl')):
        for line in open(path):
            try:
                out.append(json.loads(line))
            except ValueError:
                pass
    return sorted(out, key=lambda e: e.get('ts') or '')


def kinds():
    return [e.get('kind') for e in events()]


def run_dir():
    for d in glob.glob(os.path.join(repo, '.baton', 'runs', '*')):
        if os.path.exists(os.path.join(d, 'plan.json')):
            return d


def boundaries(path):
    out, cur = [], None
    for line in open(path):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        if e.get('isSidechain'):
            continue
        if e.get('type') == 'system' and e.get('subtype') == 'compact_boundary':
            cur = {'at': e.get('timestamp'), 'trigger': (e.get('compactMetadata') or {}).get('trigger'), 'plan_file_reference': [], 'attachments': 0}
            out.append(cur)
        elif cur is not None and e.get('type') == 'attachment':
            a = e.get('attachment') or {}
            cur['attachments'] += 1
            if a.get('type') == 'plan_file_reference':
                cur['plan_file_reference'].append(a.get('planFilePath'))
    return out


def main():
    setup()
    stores = set(glob.glob(os.path.expanduser('~/.claude/plugins/store/baton_inline*')))
    env = {'PATH': os.path.dirname(baton) + os.pathsep + os.environ['PATH'], 'BATON_HOME': os.path.join(work, 'home'),
           'GNUPGHOME': os.path.join(work, 'gnupg'), 'BATON_WORKERS': '0', 'BATON_NOTIFY_DESKTOP': '0', 'BATON_NTFY_TOPIC': '',
           'BATON_TEST_LOAD': '0', 'BATON_AUTO_REPLAN': '0'}
    d = Driver(repo, [baton, '--plugin-dir', os.path.join(REPO_ROOT, 'plugin'), '--model', 'haiku'], env_extra=env, rows=50, cols=160)
    summary = {}
    try:
        d.trust(40)
        d.wait_quiet(3, 60)
        for _ in range(6):
            if d.screen_has('plan mode on'):
                break
            d.key(b'\x1b[Z')
            time.sleep(1.0)
        d.note('plan mode: ' + str(d.screen_has('plan mode on')))
        d.type(PLAN)
        d.enter()
        approved, end = 0, time.time() + 480
        while time.time() < end and d.alive:
            ks = kinds()
            opens = ks.count('dialog_open')
            if opens > approved:
                time.sleep(1.5)
                d.key(b'\r')
                d.note(f'Enter on dialog {opens}')
                approved = opens
            if any(e.get('kind') == 'phase_started' and e.get('phase') == 'P0' for e in events()):
                d.note('P0 started')
                time.sleep(8)
                break
            time.sleep(0.5)
    finally:
        d.close()
        time.sleep(2)
        rd = run_dir()
        summary['events'] = [{k: v for k, v in e.items() if k in ('kind', 'plan', 'from', 'to', 'by', 'reason', 'phase', 'why', 'same')} for e in events()
                             if e.get('kind') in ('directives_asked', 'attached', 'plan_moved', 'plan_not_moved', 'compact_queued', 'compact_asked',
                                                  'compacted', 'brief', 'phase_started', 'phase_done', 'plan_complete', 'plan_changed')]
        if rd:
            summary['plan'] = {k: v for k, v in json.load(open(os.path.join(rd, 'plan.json'))).items() if k in ('title', 'plan_file', 'plan_origin')}
            st = json.load(open(os.path.join(rd, 'state.json')))
            tp = ((st.get('run') or {}).get('proven') or {}).get('path')
            summary['plan_looked'] = st.get('plan_looked')
            if tp and os.path.exists(tp):
                summary['transcript'] = tp
                summary['boundaries'] = boundaries(tp)
        plans = os.path.join(repo, 'plans')
        summary['plans'] = sorted(os.listdir(plans)) if os.path.isdir(plans) else None
        for p in set(glob.glob(os.path.expanduser('~/.claude/plugins/store/baton_inline*'))) - stores:
            os.remove(p)
        json.dump(summary, open(os.path.join(work, 'summary.json'), 'w'), indent=1)
        print(json.dumps(summary, indent=1))


main()
