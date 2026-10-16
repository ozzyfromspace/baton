#!/usr/bin/env python3
"""Spike 38: does renaming the session's plan-mode file stop Claude Code re-attaching it after a compaction? (Haiku)

Usage: run.py <workroot>. Runs an interactive claude (no user settings: --setting-sources project,local) in a PTY in
a fresh git repo, <workroot>/repo, whose .claude/settings.json sets plansDirectory to "plans" (inside the project, as
Claude Code requires) and logs every hook event of interest to <workroot>/hooks.jsonl (hook.py). Then, as a human
would:

  1. plan mode; a one-phase plan written to the plan file; approved with Enter (the dialog's first option);
  2. /compact (the control): the transcript after its boundary should hold a plan_file_reference;
  3. plans/<slug>.md renamed in the same folder to plans/spike-38-plan.md; a turn (Reply with OK) still runs;
  4. /compact again: the transcript after this boundary should hold none;
  5. plan mode again, a second plan: Claude Code writes a fresh plans/<slug>.md and leaves the renamed one alone;
     approved, and a third /compact attaches the fresh file again.

Prints a summary (also <workroot>/summary.json): per boundary, the attachments written after it and any
plan_file_reference's path. Stdlib only; no baton involved (the baton and Claude variables are taken out of the
child's environment, so no host's hooks answer the session).
"""
import hashlib, json, os, shlex, shutil, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver  # noqa: E402

HOOK = os.path.join(HERE, 'hook.py')
EVENTS = ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop', 'PreCompact', 'PostCompact']
PLAN = ('Plan this tiny task. Write the plan to your plan file with exactly one heading and nothing else as a heading: '
        '"## P0 — {title}" (step: reply with the single word {word}, using no tools). Do not explore the repository. '
        'Then call ExitPlanMode.')
work = os.path.abspath(sys.argv[1])
repo = os.path.join(work, 'repo')
hooks = os.path.join(work, 'hooks.jsonl')


def git(*a):
    subprocess.run(['git', '-c', 'user.name=s', '-c', 'user.email=s@s', *a], cwd=repo, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def setup():
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(os.path.join(repo, '.claude'))
    git('init', '-q', '-b', 'main')
    open(os.path.join(repo, 'README'), 'w').write('spike 38\n')
    open(os.path.join(repo, '.gitignore'), 'w').write('screen.log\n')
    git('add', '.')
    git('commit', '-qm', 'init')
    hk = {ev: [{'hooks': [{'type': 'command', 'command': f'python3 {shlex.quote(HOOK)} {ev} {shlex.quote(hooks)}'}]}]
          for ev in EVENTS}
    json.dump({'plansDirectory': 'plans', 'hooks': hk}, open(os.path.join(repo, '.claude', 'settings.json'), 'w'))


def recs():
    try:
        return [json.loads(line) for line in open(hooks) if line.strip()]
    except FileNotFoundError:
        return []


def count(ev, tool=None):
    return sum(1 for r in recs() if r['ev'] == ev and (tool is None or r.get('tool') == tool))


def mark(d, step):
    with open(hooks, 'a') as f:
        f.write(json.dumps({'t': round(time.time(), 3), 'hms': time.strftime('%H:%M:%S'), 'ev': 'DRIVER', 'step': step}) + '\n')
    d.note(step)


def plan_mode(d):
    for _ in range(6):
        if d.screen_has('plan mode on'):
            return True
        d.key(b'\x1b[Z')
        time.sleep(1.0)
    return d.screen_has('plan mode on')


def dump(d, label):
    with d.lock:
        t = d.tail[-3000:].decode('utf-8', 'replace')
    open(os.path.join(work, f'screen-{label}.txt'), 'w').write(t)


def turn(d, text, timeout=150):
    n = count('Stop')
    mark(d, 'type: ' + text[:100])
    d.type(text)
    d.enter()
    ok = d.wait(lambda: count('Stop') > n, timeout, 'Stop')
    d.wait_quiet(2, 20)
    return ok


def approve_plan(d, title, word):
    n_pre, n_post = count('PreToolUse', 'ExitPlanMode'), count('PostToolUse', 'ExitPlanMode')
    if not plan_mode(d):
        mark(d, 'could not switch to plan mode')
        return None
    mark(d, 'plan mode on: asking for ' + title)
    d.type(PLAN.format(title=title, word=word))
    d.enter()
    if not d.wait(lambda: count('PreToolUse', 'ExitPlanMode') > n_pre, 180, 'ExitPlanMode'):
        return None
    time.sleep(3)
    d.wait_quiet(1.5, 20)
    dump(d, 'dialog-' + word)
    pending = [r.get('file') for r in recs() if r['ev'] == 'PreToolUse' and r.get('tool') == 'ExitPlanMode'][-1]
    mark(d, f'approving with Enter (plan file on the dialog: {pending})')
    d.key(b'\r')
    d.wait(lambda: count('PostToolUse', 'ExitPlanMode') > n_post, 60, 'PostToolUse ExitPlanMode')
    files = [r.get('file') for r in recs() if r['ev'] == 'PostToolUse' and r.get('tool') == 'ExitPlanMode']
    n = count('Stop')
    d.wait(lambda: count('Stop') > n, 150, 'Stop after the approved plan')
    d.wait_quiet(2, 20)
    return files[-1] if files else pending


def compact(d):
    n = count('PostCompact')
    mark(d, 'type: /compact')
    d.type('/compact')
    d.enter()
    ok = d.wait(lambda: count('PostCompact') > n, 240, 'PostCompact')
    time.sleep(5)  # the attachments are written just after the boundary
    d.wait_quiet(2, 30)
    return ok


def transcript():
    for r in reversed(recs()):
        if r.get('transcript'):
            return r['transcript']


def boundaries(path):
    """Per compact_boundary of the main conversation: the attachment types after it, and any plan reference."""
    out, cur, slugs = [], None, []
    for line in open(path):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        if e.get('slug') and e['slug'] not in slugs:
            slugs.append(e['slug'])
        if e.get('isSidechain'):
            continue
        if e.get('type') == 'system' and e.get('subtype') == 'compact_boundary':
            cur = {'at': e.get('timestamp'), 'attachments': [], 'plan_file_reference': []}
            out.append(cur)
        elif cur is not None and e.get('type') == 'attachment':
            a = e.get('attachment') or {}
            cur['attachments'].append(a.get('type'))
            if a.get('type') == 'plan_file_reference':
                cur['plan_file_reference'].append({'path': a.get('planFilePath'), 'chars': len(a.get('planContent') or '')})
    return out, slugs


def sha(path):
    try:
        return hashlib.sha256(open(path, 'rb').read()).hexdigest()[:12]
    except FileNotFoundError:
        return None


def main():
    setup()
    version = subprocess.run(['claude', '--version'], capture_output=True, text=True).stdout.strip()
    d = Driver(repo, ['claude', '--model', 'haiku', '--setting-sources', 'project,local'], rows=50, cols=160)
    summary = {'claude': version, 'steps': []}

    def step(name, **kv):
        kv['step'] = name
        summary['steps'].append(kv)
        mark(d, f'{name}: {json.dumps(kv)}')

    try:
        d.trust(30)
        d.wait_quiet(3, 40)
        file1 = approve_plan(d, 'Say hello', 'hello')
        plans = os.path.join(repo, 'plans')
        step('approved', file=file1, exists=bool(file1 and os.path.exists(file1)), plans=sorted(os.listdir(plans)) if os.path.isdir(plans) else None)
        if not file1:
            return
        step('compact (control)', ok=compact(d))
        moved = os.path.join(os.path.dirname(file1), 'spike-38-plan.md')
        os.rename(file1, moved)
        step('renamed', to=moved, plans=sorted(os.listdir(plans)), moved_sha=sha(moved))
        step('turn after the rename', ok=turn(d, 'Reply with only the word OK.'))
        step('compact (renamed)', ok=compact(d))
        step('turn after the compaction', ok=turn(d, 'Reply with only the word OK.'))
        file2 = approve_plan(d, 'Say goodbye', 'goodbye')
        step('approved again', file=file2, same_path=file2 == file1, exists=bool(file2 and os.path.exists(file2)),
             plans=sorted(os.listdir(plans)), moved_sha=sha(moved), fresh_head=open(file2).read()[:200] if file2 and os.path.exists(file2) else None)
        step('compact (fresh plan file)', ok=compact(d))
    finally:
        d.type('/exit')
        d.enter()
        time.sleep(4)
        d.close()
        path = transcript()
        if path and os.path.exists(path):
            summary['transcript'] = path
            summary['boundaries'], summary['slugs'] = boundaries(path)
        json.dump(summary, open(os.path.join(work, 'summary.json'), 'w'), indent=1)
        print(json.dumps(summary, indent=1))


main()
