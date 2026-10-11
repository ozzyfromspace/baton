"""Minimal VT100/xterm emulator: enough to judge whether a byte stream paints a full screen."""
import re, unicodedata
class Screen:
    def __init__(self, rows, cols):
        self.rows, self.cols = rows, cols
        self.main = [[' ']*cols for _ in range(rows)]
        self.alt = None
        self.buf = self.main
        self.r = self.c = 0
        self.saved = (0, 0)
        self.top, self.bot = 0, rows-1
        self.scrollback = []
        self.modes = {}
        self.events = []   # notable sequences seen
        self.pending = b''
    def resize(self, rows, cols):
        def fit(b):
            b = [ (line + [' ']*cols)[:cols] for line in b ]
            if len(b) > rows: 
                self.scrollback += [''.join(l) for l in b[:len(b)-rows]]
                b = b[len(b)-rows:]
            while len(b) < rows: b.append([' ']*cols)
            return b
        self.main = fit(self.main)
        if self.alt is not None: self.alt = fit(self.alt)
        self.buf = self.alt if self.alt is not None and self.modes.get(1049) else self.main
        self.rows, self.cols = rows, cols
        self.top, self.bot = 0, rows-1
        self.r = min(self.r, rows-1); self.c = min(self.c, cols-1)
    def scroll_up(self, n=1):
        for _ in range(n):
            line = self.buf.pop(self.top)
            if self.buf is self.main and self.top == 0: self.scrollback.append(''.join(line))
            self.buf.insert(self.bot, [' ']*self.cols)
    def scroll_down(self, n=1):
        for _ in range(n):
            self.buf.pop(self.bot); self.buf.insert(self.top, [' ']*self.cols)
    def lf(self):
        if self.r == self.bot: self.scroll_up()
        elif self.r < self.rows-1: self.r += 1
    def put(self, ch):
        w = 2 if unicodedata.east_asian_width(ch) in ('W','F') else (0 if unicodedata.combining(ch) else 1)
        if w == 0: return
        if self.c >= self.cols:
            self.c = 0; self.lf()
        self.buf[self.r][self.c] = ch
        if w == 2 and self.c+1 < self.cols: self.buf[self.r][self.c+1] = ''
        self.c += w
    def feed(self, data: bytes):
        data = self.pending + data; self.pending = b''
        s = data.decode('utf-8', 'replace')
        i, n = 0, len(s)
        while i < n:
            ch = s[i]
            if ch == '\x1b':
                if i+1 >= n: self.pending = s[i:].encode(); return
                nx = s[i+1]
                if nx == '[':
                    m = re.match(r'\x1b\[([?>=<]?)([0-9;:]*)([ -/]*)([@-~])', s[i:])
                    if not m: self.pending = s[i:].encode(); return
                    self.csi(m.group(1), m.group(2), m.group(3), m.group(4)); i += m.end(); continue
                if nx == ']' or nx == 'P' or nx == '_' or nx == '^':
                    # OSC/DCS/APC: until BEL or ST
                    j = i+2
                    while j < n and s[j] != '\x07' and not (s[j] == '\x1b' and j+1 < n and s[j+1] == '\\'): j += 1
                    if j >= n: self.pending = s[i:].encode(); return
                    i = j + (1 if s[j] == '\x07' else 2); continue
                if nx in '()*+': i += 3; continue
                if nx == '7': self.saved = (self.r, self.c)
                elif nx == '8': self.r, self.c = self.saved
                elif nx == 'M':
                    if self.r == self.top: self.scroll_down()
                    else: self.r = max(0, self.r-1)
                elif nx == 'c': self.events.append('RIS'); self.__init__(self.rows, self.cols)
                i += 2; continue
            if ch == '\r': self.c = 0
            elif ch == '\n': self.lf()
            elif ch == '\b': self.c = max(0, self.c-1)
            elif ch == '\t': self.c = min(self.cols-1, (self.c//8+1)*8)
            elif ch == '\x07' or ch < ' ': pass
            else: self.put(ch)
            i += 1
    def csi(self, pfx, params, inter, final):
        ps = [int(x) if x.isdigit() else 0 for x in params.replace(':',';').split(';')] if params else []
        p0 = ps[0] if ps else 0
        def d(v): return v if v else 1
        if pfx == '?' and final in 'hl':
            for p in ps:
                on = final == 'h'; self.modes[p] = on
                if p in (1049, 1047, 47):
                    self.events.append(f'alt{"on" if on else "off"}')
                    if on:
                        self.alt = [[' ']*self.cols for _ in range(self.rows)]; self.buf = self.alt
                    else:
                        self.buf = self.main
            return
        if pfx: return
        if final == 'A': self.r = max(0, self.r-d(p0))
        elif final == 'B': self.r = min(self.rows-1, self.r+d(p0))
        elif final == 'C': self.c = min(self.cols-1, self.c+d(p0))
        elif final == 'D': self.c = max(0, self.c-d(p0))
        elif final == 'E': self.r = min(self.rows-1, self.r+d(p0)); self.c = 0
        elif final == 'F': self.r = max(0, self.r-d(p0)); self.c = 0
        elif final == 'G': self.c = min(self.cols-1, d(p0)-1)
        elif final == 'd': self.r = min(self.rows-1, d(p0)-1)
        elif final in 'Hf':
            self.r = min(self.rows-1, d(ps[0] if ps else 0)-1); self.c = min(self.cols-1, d(ps[1] if len(ps)>1 else 0)-1)
        elif final == 'J':
            if p0 == 0:
                self.buf[self.r][self.c:] = [' ']*(self.cols-self.c)
                for k in range(self.r+1, self.rows): self.buf[k] = [' ']*self.cols
            elif p0 == 1:
                for k in range(0, self.r): self.buf[k] = [' ']*self.cols
                self.buf[self.r][:self.c+1] = [' ']*(self.c+1)
            elif p0 == 2:
                self.events.append('ED2')
                for k in range(self.rows): self.buf[k] = [' ']*self.cols
            elif p0 == 3:
                self.events.append('ED3-clear-scrollback'); self.scrollback = []
        elif final == 'K':
            if p0 == 0: self.buf[self.r][self.c:] = [' ']*(self.cols-self.c)
            elif p0 == 1: self.buf[self.r][:self.c+1] = [' ']*(self.c+1)
            else: self.buf[self.r] = [' ']*self.cols
        elif final == 'X':
            for k in range(self.c, min(self.cols, self.c+d(p0))): self.buf[self.r][k] = ' '
        elif final == 'P':
            line = self.buf[self.r]; del line[self.c:self.c+d(p0)]; line += [' ']*(self.cols-len(line))
        elif final == '@':
            line = self.buf[self.r]
            for _ in range(d(p0)): line.insert(self.c, ' ')
            del line[self.cols:]
        elif final == 'L':
            for _ in range(d(p0)): self.buf.pop(self.bot); self.buf.insert(self.r, [' ']*self.cols)
        elif final == 'M':
            for _ in range(d(p0)): self.buf.pop(self.r); self.buf.insert(self.bot, [' ']*self.cols)
        elif final == 'S': self.scroll_up(d(p0))
        elif final == 'T': self.scroll_down(d(p0))
        elif final == 'r':
            self.top = d(ps[0] if ps else 0)-1; self.bot = (ps[1] if len(ps)>1 and ps[1] else self.rows)-1; self.r = self.c = 0
    def text(self):
        return '\n'.join(''.join(l).rstrip() for l in self.buf)
