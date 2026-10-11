package host

import (
	"fmt"
	"strconv"
	"sync"
)

// TailSize is how much of a hidden session's latest output baton keeps.
const TailSize = 256 << 10

// A hidden session's screen is nobody's terminal, so baton keeps what it needs to show it to a human who
// attaches later (spike 21): the tail of its output, and the state of the terminal modes it has set. A
// repaint, which a resize brings about, redraws the visible screen and re-sends the input modes, but not
// the modes Claude Code sets only once at startup (focus reporting, ?1004h; ?2031h). Those would be lost
// with the head of the output, so they are tracked apart from it.

// ring holds the last len(buf) bytes written to it.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	w    int  // where the next byte goes
	full bool // buf has wrapped: the oldest byte is at w
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if n >= len(r.buf) {
		copy(r.buf, p[n-len(r.buf):])
		r.w, r.full = 0, true
		return n, nil
	}
	c := copy(r.buf[r.w:], p)
	if c < n {
		copy(r.buf, p[c:])
		r.full = true
	}
	r.w += n
	if r.w >= len(r.buf) {
		r.w -= len(r.buf)
		r.full = true
	}
	return n, nil
}

// Bytes is a copy of what the ring holds, oldest first.
func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.w]...)
	}
	return append(append(make([]byte, 0, len(r.buf)), r.buf[r.w:]...), r.buf[:r.w]...)
}

// csiMax bounds a control sequence modeTracker reads; anything longer is not one it tracks.
const csiMax = 64

// syncOutput is synchronized output (?2026), which Claude Code turns on and off around every frame. It is
// never replayed: a viewer left with it on would show nothing until the next frame ended.
const syncOutput = 2026

// modeTracker follows the DEC private modes (ESC [ ? Pm h, ESC [ ? Pm l) a session's output sets and
// resets, so a viewer can be put into the same ones. It reads the output as it comes, so a sequence split
// across reads is still seen.
type modeTracker struct {
	mu    sync.Mutex
	esc   []byte // a sequence in progress, from its ESC
	order []int  // the modes seen, in the order first seen
	set   map[int]bool
}

func (m *modeTracker) feed(b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range b {
		switch {
		case c == 0x1b:
			m.esc = append(m.esc[:0], c)
		case len(m.esc) == 0:
		case len(m.esc) == 1:
			if c == '[' {
				m.esc = append(m.esc, c)
			} else {
				m.esc = m.esc[:0]
			}
		case c >= 0x40 && c <= 0x7e: // a CSI sequence's final byte
			m.finish(m.esc[2:], c)
			m.esc = m.esc[:0]
		case len(m.esc) >= csiMax:
			m.esc = m.esc[:0]
		default:
			m.esc = append(m.esc, c)
		}
	}
}

// finish records a CSI sequence with parameters params and final byte final, if it sets or resets DEC
// private modes.
func (m *modeTracker) finish(params []byte, final byte) {
	if (final != 'h' && final != 'l') || len(params) < 2 || params[0] != '?' {
		return
	}
	var modes []int
	for _, f := range splitParams(params[1:]) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return
		}
		modes = append(modes, n)
	}
	if m.set == nil {
		m.set = map[int]bool{}
	}
	for _, n := range modes {
		if n == syncOutput {
			continue
		}
		if _, seen := m.set[n]; !seen {
			m.order = append(m.order, n)
		}
		m.set[n] = final == 'h'
	}
}

func splitParams(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == ';' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return append(out, string(b[start:]))
}

// sequences is what puts a terminal into the modes seen, each in its latest state.
func (m *modeTracker) sequences() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []byte
	for _, n := range m.order {
		f := 'l'
		if m.set[n] {
			f = 'h'
		}
		out = fmt.Appendf(out, "\x1b[?%d%c", n, f)
	}
	return out
}
