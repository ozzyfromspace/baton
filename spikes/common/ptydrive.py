"""Drive an interactive `claude` in a pseudo-terminal. Answers the folder-trust prompt, stops when done or on timeout."""
import os, pty, sys, time, select, re, json, fcntl, termios, struct, signal
cwd, timeout, done_file = sys.argv[1], float(sys.argv[2]), sys.argv[3]
args = sys.argv[4:]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(cwd)
    for k in list(os.environ):
        if k.startswith('CLAUDE'):
            del os.environ[k]
    os.environ['TERM'] = 'xterm-256color'
    os.execvp(args[0], args)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 50, 160, 0, 0))
screen = open(os.path.join(cwd, 'screen.log'), 'wb')
ansi = re.compile(rb'\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][0-9A-Za-z]|\x1b[=>]')
buf = b''; start = time.time(); trusted = False; done_at = None
while time.time() - start < timeout:
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            data = os.read(fd, 65536)
        except OSError:
            break
        if not data:
            break
        clean = ansi.sub(b'', data)
        screen.write(clean); screen.flush()
        buf = (buf + clean)[-8000:]
        low = buf.lower()
        if not trusted and b'trustthisfolder' in low.replace(b' ', b'') and b'enter' in low:
            time.sleep(0.8); os.write(fd, b'\x1b[B'); time.sleep(0.4); os.write(fd, b'\r'); trusted = True; buf = b''
            screen.write(b'\n[driver] answered trust prompt\n')
    inj = os.path.join(cwd, 'inject.txt')
    if os.path.exists(inj):
        txt = open(inj).read().strip(); os.remove(inj)
        for ch in txt:
            os.write(fd, ch.encode()); time.sleep(0.03)
        time.sleep(0.5); os.write(fd, b'\r')
        screen.write(f'\n[driver] injected: {txt}\n'.encode())
    if os.path.exists(done_file) and done_at is None:
        try:
            st = json.load(open(done_file))
        except Exception:
            st = {}
        if st.get('p2_done'):
            done_at = time.time()
    if done_at and time.time() - done_at > 15:
        break
os.kill(pid, signal.SIGTERM); time.sleep(1)
try: os.kill(pid, signal.SIGKILL)
except Exception: pass
print('driver finished after', round(time.time() - start), 's; done' if done_at else '; NOT done')
