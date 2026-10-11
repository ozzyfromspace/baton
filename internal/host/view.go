package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// A human watches a hidden session from a terminal of their own with baton view, which connects to the
// host's view socket and asks for the session by name. The host puts the viewer's terminal in the modes
// the session's screen is in, clears it, and sizes the session's terminal to the viewer's: claude repaints
// its whole screen on a resize (spike 21). From then on the viewer gets everything the session writes, and
// its keys go to the session as a human's. One viewer at a time per session; when it detaches, the
// session's terminal goes back to its own size.

// Frames between a viewer and the host: a type byte, the payload's length (4 bytes, big-endian), then the
// payload.
const (
	frameAttach = 'a' // viewer → host: its rows and cols (2 bytes each), then the session's name
	frameData   = 'd' // host → viewer: the session's output; viewer → host: keys
	frameResize = 'r' // viewer → host: its new rows and cols
	frameDetach = 'x' // viewer → host: it leaves; host → viewer: it is detached, and why
)

const (
	frameMax = 1 << 20
	// viewBacklog bounds the output waiting for a viewer, in reads of the session's terminal. A viewer that
	// falls further behind is detached: a hidden session never waits on its viewer.
	viewBacklog = 256
	// viewTimeout bounds a viewer's attach request and each write to it.
	viewTimeout = 10 * time.Second
	// nudgeDelay separates the two resizes that repaint a session whose terminal already has the
	// viewer's size: claude ignores a resize to the size it has.
	nudgeDelay = 150 * time.Millisecond
)

// viewerReset takes a viewer's terminal out of the modes claude's screen put it in, for the shell it goes
// back to: bracketed paste, focus and colour-scheme reports, mouse reports, the kitty keyboard protocol
// and xterm's modifyOtherKeys, a hidden cursor, and colours.
const viewerReset = "\x1b[?2004l\x1b[?1004l\x1b[?2031l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" +
	"\x1b[<99u\x1b[>4m\x1b[?25h\x1b[0m"

// ViewSocket is where the host whose instance is instance listens for viewers. It is in a directory of
// the user's own under the temporary directory, not under $BATON_HOME: macOS caps a socket's path at 104
// bytes.
func ViewSocket(instance string) string {
	dir := "baton"
	if uid := os.Getuid(); uid >= 0 {
		dir += "-" + strconv.Itoa(uid)
	}
	return filepath.Join(os.TempDir(), dir, instance+".sock")
}

func writeFrame(w io.Writer, t byte, p []byte) error {
	b := make([]byte, 5, 5+len(p))
	b[0] = t
	binary.BigEndian.PutUint32(b[1:], uint32(len(p)))
	_, err := w.Write(append(b, p...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > frameMax {
		return 0, nil, fmt.Errorf("a frame of %d bytes", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

func sizeBytes(rows, cols uint16) []byte {
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, rows), cols)
}

// listenViews starts taking viewers, when the first hidden session starts, if the supervisor has a view
// socket. Viewing is a convenience: if it cannot listen, that is logged and the sessions run as usual.
// sv.mu is held.
func (sv *Supervisor) listenViews() {
	if sv.views != nil || sv.cfg.ViewSocket == "" {
		return
	}
	l, err := listenUnix(sv.cfg.ViewSocket)
	if err != nil {
		sv.cfg.Logf("host: viewers cannot attach: %v", err)
		return
	}
	sv.views = l
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go sv.serveView(c)
		}
	}()
}

// listenUnix listens at path, in a directory nobody else can enter: whoever reaches the socket can type
// into the sessions behind it.
func listenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory of its own", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // fails unless the directory is the user's
		return nil, err
	}
	os.Remove(path) // left by a host that was killed: an instance is never reused
	return net.Listen("unix", path)
}

// closeViews stops taking viewers and removes the socket.
func (sv *Supervisor) closeViews() {
	sv.mu.Lock()
	l := sv.views
	sv.views = nil
	sv.mu.Unlock()
	if l != nil {
		l.Close()
	}
}

// serveView attaches a viewer to the hidden session it asks for, then passes its keys and size on until it
// detaches or goes away. Both are logged in the session's run.
func (sv *Supervisor) serveView(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(viewTimeout))
	t, p, err := readFrame(conn)
	if err != nil || t != frameAttach || len(p) < 4 {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	rows, cols, name := binary.BigEndian.Uint16(p), binary.BigEndian.Uint16(p[2:]), string(p[4:])
	v := newViewer(conn)
	s := sv.Session(name)
	if s == nil {
		v.finish(fmt.Sprintf("no worker on %s runs in this baton", name))
		return
	}
	if err := s.attach(v); err != nil {
		v.finish(err.Error())
		return
	}
	s.logf("a viewer attached (%dx%d)", cols, rows)
	if st := s.runOf(); st != nil {
		st.Event("viewer_attached", map[string]any{"rows": rows, "cols": cols})
	}
	s.fit(v, rows, cols)
	for {
		t, p, err := readFrame(conn)
		if err != nil || t == frameDetach {
			break
		}
		switch {
		case t == frameData && s.viewing(v):
			s.Keys(p)
		case t == frameResize && len(p) == 4:
			s.resizeFor(v, binary.BigEndian.Uint16(p), binary.BigEndian.Uint16(p[2:]))
		}
	}
	why := "the viewer left"
	if !s.detach(v, "") {
		why = v.why // the host let it go first
	}
	s.logf("the viewer detached: %s", why)
	if st := s.runOf(); st != nil {
		st.Event("viewer_detached", map[string]any{"why": why})
	}
}

// viewer is the host's end of a viewer's connection. What the session writes is queued for it, and
// written by a goroutine of its own, so the session never waits on it.
type viewer struct {
	conn net.Conn
	out  chan []byte
	why  string // why it was detached, set before out is closed
}

func newViewer(conn net.Conn) *viewer {
	v := &viewer{conn: conn, out: make(chan []byte, viewBacklog)}
	go v.write()
	return v
}

// send queues b for the viewer, unless it is too far behind to take it.
func (v *viewer) send(b []byte) bool {
	select {
	case v.out <- bytes.Clone(b):
		return true
	default:
		return false
	}
}

// finish detaches the viewer, telling it why once it has what was queued before.
func (v *viewer) finish(why string) {
	v.why = why
	close(v.out)
}

func (v *viewer) write() {
	defer v.conn.Close()
	for b := range v.out {
		v.conn.SetWriteDeadline(time.Now().Add(viewTimeout))
		if writeFrame(v.conn, frameData, b) != nil {
			return
		}
	}
	v.conn.SetWriteDeadline(time.Now().Add(viewTimeout))
	writeFrame(v.conn, frameDetach, []byte(v.why))
}

// attach makes v the viewer of a hidden session. Its terminal is first put in the modes the session's
// screen is in, and cleared; what the session writes from then on follows.
func (s *Session) attach(v *viewer) error {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	switch {
	case !s.Hidden():
		return errors.New("only a worker can be viewed")
	case s.ended():
		return fmt.Errorf("%s has ended", s.Name)
	case s.viewer != nil:
		return fmt.Errorf("%s already has a viewer: one at a time", s.Name)
	}
	s.viewer = v
	v.send(append(s.Modes(), "\x1b[H\x1b[2J"...))
	return nil
}

// toViewer passes a hidden session's output to its viewer, if it has one. s.viewMu is held.
func (s *Session) toViewer(b []byte) {
	if s.viewer != nil && !s.viewer.send(b) {
		s.viewer.finish("baton: the viewer fell too far behind")
		s.viewer = nil
		s.restoreSize()
	}
}

// viewing reports whether v is the session's viewer.
func (s *Session) viewing(v *viewer) bool {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	return s.viewer == v
}

// detach lets go of v, if it is still the session's viewer, and gives the session's terminal its own
// size back. It reports whether v was the viewer.
func (s *Session) detach(v *viewer, why string) bool {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if s.viewer != v {
		return false
	}
	s.viewer = nil
	v.finish(why)
	s.restoreSize()
	return true
}

// endViewer detaches the viewer of a session that has ended.
func (s *Session) endViewer() {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if s.viewer != nil {
		s.viewer.finish(fmt.Sprintf("%s ended (exit code %d)", s.Name, s.code))
		s.viewer = nil
	}
}

// restoreSize gives a session with no viewer its own size back. s.viewMu is held.
func (s *Session) restoreSize() {
	if r, c := s.size(); !s.ended() && (r != s.ownRows || c != s.ownCols) {
		s.Resize(s.ownRows, s.ownCols)
	}
}

// fit sizes the session's terminal to its new viewer's, which makes claude repaint its whole screen there.
// At the size it already has, the width is nudged first.
func (s *Session) fit(v *viewer, rows, cols uint16) {
	if r, c := s.size(); r == rows && c == cols {
		nudge := cols - 1
		if cols < 2 {
			nudge = cols + 1
		}
		s.resizeFor(v, rows, nudge)
		time.Sleep(nudgeDelay)
	}
	s.resizeFor(v, rows, cols)
}

// resizeFor sizes the session's terminal for v, as long as v is its viewer.
func (s *Session) resizeFor(v *viewer, rows, cols uint16) {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if s.viewer == v && rows > 0 && cols > 0 {
		s.Resize(rows, cols)
	}
}

// ErrNoTerminal means baton view was run without a terminal to show a session in.
var ErrNoTerminal = errors.New("baton view needs a terminal")

// Watch shows the hidden session name, of the host listening at path, in the terminal in and out are, and
// passes the human's keys to it, until they press Ctrl-] (it returns "") or the host detaches the viewer
// (it returns why). Meanwhile the terminal is raw, and the session follows its size; it is then put back
// as it was.
func Watch(path, name string, in, out *os.File) (string, error) {
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return "", ErrNoTerminal
	}
	cols, rows, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return "", err
	}
	v, err := Dial(path, name, uint16(rows), uint16(cols))
	if err != nil {
		return "", err
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		v.conn.Close()
		return "", err
	}
	defer term.Restore(int(in.Fd()), old)
	defer OnResize(func() {
		if c, r, err := term.GetSize(int(out.Fd())); err == nil {
			v.Resize(uint16(r), uint16(c))
		}
	})()
	why, err := v.Run(in, out)
	out.WriteString(viewerReset + "\x1b[999;1H\r\n") // below the session's screen, for the shell
	return why, err
}

// Viewer is baton view's end of a hidden session: it shows the session's screen in the human's terminal,
// and passes their keys to it.
type Viewer struct {
	conn  net.Conn
	first []byte // the first of the screen, read while attaching
	wmu   sync.Mutex
}

// Dial attaches to the hidden session name of the host listening at path, for a terminal of rows x cols.
// The error says why if the host refused.
func Dial(path, name string, rows, cols uint16) (*Viewer, error) {
	c, err := net.DialTimeout("unix", path, viewTimeout)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(c, frameAttach, append(sizeBytes(rows, cols), name...)); err != nil {
		c.Close()
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(viewTimeout))
	t, p, err := readFrame(c)
	c.SetReadDeadline(time.Time{})
	switch {
	case err != nil:
		c.Close()
		return nil, err
	case t == frameDetach:
		c.Close()
		return nil, errors.New(string(p))
	}
	return &Viewer{conn: c, first: p}, nil
}

// Resize tells the host the viewer's terminal is now rows x cols.
func (v *Viewer) Resize(rows, cols uint16) error { return v.send(frameResize, sizeBytes(rows, cols)) }

func (v *Viewer) send(t byte, p []byte) error {
	v.wmu.Lock()
	defer v.wmu.Unlock()
	return writeFrame(v.conn, t, p)
}

// Run shows the session's screen on out and passes the keys read from in to it, until the human presses
// Ctrl-] or in ends (it returns ""), or the host detaches the viewer (it returns why).
func (v *Viewer) Run(in io.Reader, out io.Writer) (string, error) {
	defer v.conn.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			keys, leave := buf[:n], err != nil
			if i := detachKey(keys); i >= 0 {
				keys, leave = keys[:i], true
			}
			if len(keys) > 0 && v.send(frameData, keys) != nil {
				return
			}
			if leave {
				v.send(frameDetach, nil)
				// The host answers with a detach of its own; one that does not is gone.
				v.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				return
			}
		}
	}()
	if _, err := out.Write(v.first); err != nil {
		return "", err
	}
	for {
		t, p, err := readFrame(v.conn)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return "", nil
			}
			return "", err
		}
		switch t {
		case frameData:
			if _, err := out.Write(p); err != nil {
				return "", err
			}
		case frameDetach:
			return string(p), nil
		}
	}
}

// detachKey finds Ctrl-] in keys from the viewer's terminal, and returns where it starts or -1. It comes
// as a plain 0x1d, or, once claude's screen has turned them on in the viewer's terminal, as the kitty
// keyboard protocol (CSI 93;5u) or xterm's modifyOtherKeys (CSI 27;5;93~) report it.
func detachKey(b []byte) int {
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == 0x1d:
			return i
		case b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[':
			end := i + 2
			for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
				end++
			}
			if end == len(b) {
				return -1
			}
			if ctrlBracket(string(b[i+2:end]), b[end]) {
				return i
			}
			i = end
		}
	}
	return -1
}

// ctrlBracket reports whether a CSI sequence's parameters and final byte are Ctrl-] pressed.
func ctrlBracket(params string, final byte) bool {
	f := strings.Split(params, ";")
	sub := func(s string, i int) string {
		p := strings.Split(s, ":")
		if i < len(p) {
			return p[i]
		}
		return ""
	}
	// Ctrl alone, whatever the lock keys: modifiers are 1 + a bit mask, in which Ctrl is 4, Caps Lock 64
	// and Num Lock 128.
	ctrl := func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= 1 && (n-1)&^(64|128) == 4
	}
	switch final {
	case 'u': // key[:alternates];modifiers[:event][;text]
		if len(f) < 2 {
			return false
		}
		ev := sub(f[1], 1)
		return sub(f[0], 0) == "93" && ctrl(sub(f[1], 0)) && (ev == "" || ev == "1")
	case '~': // 27;modifiers;key
		return len(f) == 3 && f[0] == "27" && ctrl(f[1]) && f[2] == "93"
	}
	return false
}
