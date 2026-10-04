#!/usr/bin/env python3
"""Spike prototype: host `claude` in a pseudo-terminal, pass every byte through, and type /compact when a hook asks.

Throwaway (Python stdlib, Unix only). The real thing would be a compiled binary.
"""
import errno, fcntl, os, pty, select, signal, struct, sys, termios, time, tty

STATE_DIR = os.path.abspath(os.environ.get('SC_DIR') or '.sc')
os.makedirs(STATE_DIR, exist_ok=True)
REQ, ACK = os.path.join(STATE_DIR, 'compact.request'), os.path.join(STATE_DIR, 'compact.ack')
LOG = os.path.join(STATE_DIR, 'wrapper.log')
QUIET_S = 1.5        # the screen must have been still this long before typing
HANDS_OFF_S = 3.0    # and the human must not have typed for this long
ACK_TIMEOUT_S = 15   # PreCompact must confirm within this long, or it counts as a miss
MAX_TRIES = 3

def log(msg):
    with open(LOG, 'a') as f:
        f.write(f"{time.strftime('%H:%M:%S')} {msg}\n")

pid, master = pty.fork()
if pid == 0:
    os.environ['SC_DIR'] = STATE_DIR
    os.execvp('claude', ['claude', *sys.argv[1:]])

interactive = os.isatty(0)

def sync_size(*_):
    if interactive:
        try:
            fcntl.ioctl(master, termios.TIOCSWINSZ, fcntl.ioctl(0, termios.TIOCGWINSZ, b'\0' * 8))
        except OSError:
            pass

sync_size()
signal.signal(signal.SIGWINCH, sync_size)
saved = termios.tcgetattr(0) if interactive else None
if interactive:
    tty.setraw(0)
log(f'wrapper started, child pid {pid}')

last_out = last_key = 0.0
tries, awaiting_since = 0, None
try:
    while True:
        try:
            ready, _, _ = select.select([0, master], [], [], 0.2)
        except InterruptedError:
            continue
        now = time.time()
        if master in ready:
            try:
                data = os.read(master, 65536)
            except OSError as e:
                if e.errno == errno.EIO:
                    break
                raise
            if not data:
                break
            os.write(1, data)
            last_out = now
        if 0 in ready:
            data = os.read(0, 4096)
            if data:
                os.write(master, data)
                last_key = now
        # The one thing the wrapper does on its own: type /compact when asked, then demand proof it happened.
        if awaiting_since is not None:
            if os.path.exists(ACK):
                log(f'confirmed: PreCompact fired ({round(now - awaiting_since, 1)}s after typing)')
                for p in (ACK, REQ):
                    if os.path.exists(p): os.remove(p)
                awaiting_since, tries = None, 0
            elif now - awaiting_since > ACK_TIMEOUT_S:
                log(f'MISS: no PreCompact within {ACK_TIMEOUT_S}s of try {tries}')
                awaiting_since = None
        elif os.path.exists(REQ) and now - last_out >= QUIET_S and now - last_key >= HANDS_OFF_S:
            if tries >= MAX_TRIES:
                log('ESCALATE: giving up after max tries')
                os.replace(REQ, REQ + '.failed')
                continue
            tries += 1
            log(f'screen quiet {round(now - last_out, 1)}s, hands off {round(now - last_key, 1)}s: typing /compact (try {tries})')
            for ch in '/compact':
                os.write(master, ch.encode()); time.sleep(0.03)
            time.sleep(0.4)
            os.write(master, b'\r')
            awaiting_since = time.time()
finally:
    if saved is not None:
        termios.tcsetattr(0, termios.TCSAFLUSH, saved)
    _, status = os.waitpid(pid, 0)
    log(f'child exited with status {status}')
sys.exit(os.waitstatus_to_exitcode(status) if hasattr(os, 'waitstatus_to_exitcode') else 0)
