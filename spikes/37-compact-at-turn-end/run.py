#!/usr/bin/env python3
"""Spike 37: is $.session.compact taken from the lead's turn.complete hook? (Haiku, one prompt per run)

Usage: run.py <mode> <workroot> [seconds]. Runs an interactive claude in a PTY in <workroot>/repo, with only this
spike's mod (mod/, loaded with --plugin-dir) and no user settings (--setting-sources project,local), answers the
folder's trust dialog, types one prompt ("Reply with only the word OK."), waits [seconds] (70), and prints the mod's
log (<workroot>/<mode>.log, the screen's bytes beside it). The mod's SPK_MODE says when it calls $.session.compact:

  poll       from a timer set at session.start, while the lead's Stop hooks run (a 1.5 s mod Stop hook stands in
             for a slow settings one), as baton's poll does; refused, it is called again from the lead's
             turn.complete, after next(e): the mod's hold
  held       the same, from a timer started inside the mod's classic.Stop hook
  before     from the lead's turn.complete hook, before next(e)
  after      from the lead's turn.complete hook, after next(e)
  timer      from a $.clock.after(0) started in the lead's turn.complete hook
  stop       from the mod's classic.Stop hook, after next(e)
  stoptimer  from a $.clock.after(0) started in the mod's classic.Stop hook, after next(e)

Stdlib only; no baton involved. The baton variables are taken out of the environment, so no baton host's hooks
answer the session.
"""
import os, pty, select, signal, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
mode, work = sys.argv[1], os.path.abspath(sys.argv[2])
wait = int(sys.argv[3]) if len(sys.argv) > 3 else 70
os.makedirs(os.path.join(work, 'repo'), exist_ok=True)
log = os.path.join(work, mode + '.log')
if os.path.exists(log):
    os.remove(log)
drop = ('BATON_HOST', 'CLAUDE_CODE_SESSION_ID', 'BATON_INSTANCE', 'BATON_DIR', 'BATON_BIN', 'BATON_WORKERS', 'CLAUDECODE', 'CLAUDE_CODE_ENTRYPOINT')
env = {k: v for k, v in os.environ.items() if k not in drop}
env.update({'BATON_HOME': os.path.join(work, 'bh'), 'SPK_LOG': log, 'SPK_MODE': mode, 'TERM': 'xterm-256color', 'COLUMNS': '120', 'LINES': '40'})
argv = ['claude', '--model', 'haiku', '--setting-sources', 'project,local', '--plugin-dir', os.path.join(HERE, 'mod')]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(os.path.join(work, 'repo'))
    os.execvpe(argv[0], argv, env)

screen = open(os.path.join(work, mode + '.screen'), 'wb')


def pump(secs):
    end = time.time() + secs
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                data = os.read(fd, 65536)
            except OSError:
                return
            if not data:
                return
            screen.write(data)
            screen.flush()


def send(b):
    os.write(fd, b)


pump(8)
send(b'\x1b[B')  # the trust dialog: down to "Yes, I trust this folder"
pump(1)
send(b'\r')
pump(5)
for ch in 'Reply with only the word OK.':
    send(ch.encode())
    time.sleep(0.02)
pump(1)
send(b'\r')
pump(wait)
os.kill(pid, signal.SIGTERM)
pump(2)
try:
    os.waitpid(pid, 0)
except Exception:
    pass
print(open(log).read() if os.path.exists(log) else '(no log)')
