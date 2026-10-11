"""Q6: two workers (worktrees P5, P6) and one session in proj itself, started within a second of each other."""
import os, sys, time, json, uuid, re
from drv import Drv, report, last_answer, HERE, rel, short, git
W = [('P5', os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P5'), 'IBIS-1105'),
     ('P6', os.path.join(HERE, 'proj', '.baton', 'worktrees', 'P6'), 'KESTREL-6606'),
     ('MAIN', os.path.join(HERE, 'proj'), 'GANNET-9909')]
ds = []
for name, cwd, cw in W:
    if name == 'MAIN':
        p = ('Use Bash to run exactly: git worktree list --porcelain | grep -c worktree; git status --short -- then reply with the code word '
             'from your session context, then DONE-MAIN, then the command output.')
    else:
        p = (f'Create the file notes-{name}.txt containing the line "hello from {name}" with the Write tool. Then use Bash to run exactly: '
             f'git status --short; git branch --show-current -- then reply with the code word from your session context, then DONE-{name}, then the command output.')
    ds.append(Drv(cwd, ['--session-id', str(uuid.uuid4()), '--permission-mode', 'acceptEdits', '--allowedTools', 'Bash', '--', p],
                  os.path.join(HERE, 'logs', f'q6-{name}.log'), name=f'q6-{name}', codeword=cw))
t0 = min(d.t0 for d in ds)
for d in ds:
    d.wait(lambda: d.count('Stop') > 0 or d.trust_visible(), 150, 'Stop')
for d in ds: d.wait_quiet(2, 20)
for (name, cwd, cw), d in zip(W, ds):
    d.dump('after-stop')
    plain = d.plain().decode('utf-8', 'replace')
    flags = sorted({m.lower() for m in re.findall(r'(?i)\b(warn\w*|lock\w*|corrupt\w*|error\w*|conflict\w*|another (?:instance|session|claude)\w*)', plain)})
    st, ups = d.first('Stop'), d.first('UserPromptSubmit')
    print(f'--- {name}: trust={d.trust_visible()} UPS {rel(ups["t"], t0) if ups else None} Stop {rel(st["t"], t0) if st else None} '
          f'(s after first launch); session ids {sorted({(r.get("input") or {}).get("session_id") for r in d.recs()})}')
    print('    answer:', repr(short(last_answer(d))))
    print('    code word right:', cw in (last_answer(d) or ''), '| suspicious words on screen:', flags)
    print('    files:', sorted(f for f in os.listdir(cwd) if f.startswith('notes')))
for d in ds: d.end_exit()
for d in ds: print(d.name, 'reap:', d.reap(10))
time.sleep(1.5)
for d in ds: print(d.name, 'tree after:', d.tree())
for d in ds: report(d, d.name); d.save('run'); d.kill()
