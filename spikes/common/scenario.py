"""Scripted driver for an interactive `claude` in a pseudo-terminal (spike harness, P1).

A background thread drains the PTY into screen.log so the child never blocks;
the scenario script waits on conditions (hook-log lines, screen text) and types.
"""
import json, os, pty, re, signal, struct, fcntl, termios, threading, time

ANSI = re.compile(rb'\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]')


class Driver:
    def __init__(self, cwd, argv, env_extra=None, rows=50, cols=160):
        self.cwd = cwd
        self.screen_path = os.path.join(cwd, 'screen.log')
        self.screen = open(self.screen_path, 'wb')
        self.tail = b''
        self.last_out = time.time()
        self.lock = threading.Lock()
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            for k in list(os.environ):
                if k.startswith('CLAUDE') or k.startswith('BATON_'):  # a hosted parent's BATON_* must not leak in
                    del os.environ[k]
            os.environ['TERM'] = 'xterm-256color'
            os.environ.update(env_extra or {})
            os.execvp(argv[0], argv)
        self.pid, self.fd = pid, fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        self.alive = True
        threading.Thread(target=self._pump, daemon=True).start()

    def _pump(self):
        while True:
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                break
            if not data:
                break
            clean = ANSI.sub(b'', data)
            with self.lock:
                self.screen.write(clean); self.screen.flush()
                self.tail = (self.tail + clean)[-20000:]
                self.last_out = time.time()
        self.alive = False

    def note(self, msg):
        with self.lock:
            self.screen.write(f'\n[driver {time.strftime("%H:%M:%S")}] {msg}\n'.encode()); self.screen.flush()
        print(f'[{time.strftime("%H:%M:%S")}] {msg}', flush=True)

    def screen_has(self, needle):
        with self.lock:
            return needle.encode().replace(b' ', b'') in self.tail.replace(b' ', b'')

    def wait(self, cond, timeout, what):
        end = time.time() + timeout
        while time.time() < end:
            if cond():
                return True
            if not self.alive:
                self.note(f'child exited while waiting for: {what}')
                return False
            time.sleep(0.3)
        self.note(f'TIMEOUT waiting for: {what}')
        return False

    def wait_screen(self, needle, timeout=60):
        return self.wait(lambda: self.screen_has(needle), timeout, f'screen "{needle}"')

    def wait_quiet(self, secs=2.0, timeout=60):
        return self.wait(lambda: time.time() - self.last_out >= secs, timeout, f'{secs}s of quiet')

    def wait_log(self, path, pred, timeout=120, what='log line'):
        def check():
            try:
                return any(pred(json.loads(l)) for l in open(path) if l.strip())
            except FileNotFoundError:
                return False
        return self.wait(check, timeout, what)

    def trust(self, timeout=30):
        if self.wait_screen('trustthisfolder', timeout):
            time.sleep(0.8); os.write(self.fd, b'\x1b[B'); time.sleep(0.4); os.write(self.fd, b'\r')
            self.note('answered trust prompt')

    def type(self, text, delay=0.03):
        for ch in text:
            os.write(self.fd, ch.encode()); time.sleep(delay)

    def key(self, raw):
        os.write(self.fd, raw)

    def enter(self):
        time.sleep(0.4); os.write(self.fd, b'\r')

    def close(self):
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        time.sleep(1.5)
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def hooklog_settings(script, events, extra=None):
    """Hook config that sends every listed event to `python3 <script> <Event>`; extra = {Event: [hook entries]}."""
    hooks = {}
    for ev in events:
        hooks[ev] = [{'hooks': [{'type': 'command', 'command': f'python3 "{script}" {ev}'}]}]
    for ev, entries in (extra or {}).items():
        hooks.setdefault(ev, []).extend(entries)
    return {'hooks': hooks}
