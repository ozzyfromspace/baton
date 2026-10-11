"""Raw-recording PTY driver: every chunk is kept with its timestamp, reads can be paused, size changed."""
import os, pty, signal, struct, fcntl, termios, threading, time, json, re, select
ANSI = re.compile(rb'\x1b\[[0-9;?<>=:]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78M]')
class Drv:
    def __init__(self, cwd, argv, rows=50, cols=160, env_extra=None, name='w'):
        self.cwd, self.rows, self.cols, self.name = cwd, rows, cols, name
        self.chunks = []        # (t, bytes)
        self.marks = []         # (t, label)
        self.paused = threading.Event()
        self.lock = threading.Lock()
        self.last_out = time.time(); self.t0 = time.time()
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            for k in list(os.environ):
                if k.startswith('CLAUDE') or k.startswith('BATON_'): del os.environ[k]
            os.environ['TERM'] = 'xterm-256color'
            os.environ.update(env_extra or {})
            os.execvp(argv[0], argv)
        self.pid, self.fd = pid, fd
        self.setsize(rows, cols, signal_=False)
        self.alive = True
        threading.Thread(target=self._pump, daemon=True).start()
    def setsize(self, rows, cols, signal_=True):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        self.rows, self.cols = rows, cols
        if signal_:
            try: os.kill(self.pid, signal.SIGWINCH)
            except ProcessLookupError: pass
    def _pump(self):
        while True:
            if self.paused.is_set(): time.sleep(0.05); continue
            r, _, _ = select.select([self.fd], [], [], 0.2)
            if not r: continue
            try: data = os.read(self.fd, 65536)
            except OSError: break
            if not data: break
            with self.lock:
                self.chunks.append((time.time(), data)); self.last_out = time.time()
        self.alive = False
    def mark(self, label):
        with self.lock: self.marks.append((time.time(), label))
        print(f'[{self.name} {time.time()-self.t0:7.1f}s] {label}', flush=True)
    def plain_tail(self, n=30000):
        with self.lock: b = b''.join(c for _, c in self.chunks[-400:])
        return ANSI.sub(b'', b)[-n:]
    def has(self, needle):
        return needle.encode().replace(b' ', b'') in self.plain_tail().replace(b' ', b'')
    def wait(self, cond, timeout, what):
        end = time.time()+timeout
        while time.time() < end:
            if cond(): return True
            if not self.alive: self.mark(f'child exited waiting for {what}'); return False
            time.sleep(0.2)
        self.mark(f'TIMEOUT waiting for {what}'); return False
    def wait_has(self, needle, timeout=60): return self.wait(lambda: self.has(needle), timeout, repr(needle))
    def wait_quiet(self, secs=2.0, timeout=60): return self.wait(lambda: time.time()-self.last_out >= secs, timeout, f'{secs}s quiet')
    def type(self, text, delay=0.02):
        for ch in text: os.write(self.fd, ch.encode()); time.sleep(delay)
    def key(self, raw): os.write(self.fd, raw)
    def enter(self): time.sleep(0.4); os.write(self.fd, b'\r')
    def bytes_between(self, a, b=None):
        with self.lock: return b''.join(c for t, c in self.chunks if t >= a and (b is None or t < b))
    def save(self, path):
        with self.lock:
            json.dump({'marks': self.marks, 'chunks': [(t, c.decode('latin-1')) for t, c in self.chunks]}, open(path, 'w'))
    def close(self):
        for s in (signal.SIGTERM, signal.SIGKILL):
            try: os.kill(self.pid, s)
            except ProcessLookupError: pass
            time.sleep(1.5)
