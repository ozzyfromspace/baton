//go:build !windows

package host

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// viewSocket is a view socket for one test, at a path short enough for macOS.
func viewSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "views", "i.sock")
}

// viewSupervisor is a supervisor with no lead, whose hidden sessions take viewers at the returned socket.
func viewSupervisor(t *testing.T) (*Supervisor, string) {
	sock := viewSocket(t)
	sv := NewSupervisor(Config{Claude: fakeClaude, TypeDelay: time.Millisecond, EnterDelay: 5 * time.Millisecond,
		HangupGrace: 200 * time.Millisecond, ViewSocket: sock})
	t.Cleanup(sv.closeViews)
	return sv, sock
}

// syncBuf is a terminal a viewer writes to.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// watching is a viewer running on a pipe for its keyboard and a buffer for its screen.
type watching struct {
	t      *testing.T
	v      *Viewer
	screen *syncBuf
	keys   *os.File // what is written here is typed in the viewer's terminal
	done   chan struct{}
	why    string
	err    error
}

// watch attaches a viewer of rows x cols to the session name, and runs it.
func watch(t *testing.T, sock, name string, rows, cols uint16) *watching {
	t.Helper()
	v, err := Dial(sock, name, rows, cols)
	if err != nil {
		t.Fatalf("attaching to %s: %v", name, err)
	}
	in, keys, _ := os.Pipe()
	w := &watching{t: t, v: v, screen: &syncBuf{}, keys: keys, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		w.why, w.err = v.Run(in, w.screen)
		in.Close()
	}()
	t.Cleanup(func() { keys.Close(); <-w.done })
	return w
}

func (w *watching) waitScreen(want string) {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(w.screen.String(), want) {
		if time.Now().After(deadline) {
			w.t.Fatalf("the viewer never showed %q; got %q", want, w.screen.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wait waits for the viewer to be detached, and returns why the host said it was.
func (w *watching) wait() string {
	w.t.Helper()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		w.t.Fatal("the viewer was never detached")
	}
	if w.err != nil {
		w.t.Fatalf("the viewer failed: %v", w.err)
	}
	return w.why
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A viewer's terminal is put in the modes the worker's screen is in and cleared, then the worker's
// terminal takes the viewer's size, which makes claude repaint its whole screen there (spike 21). At the
// size it has, the width is nudged, since claude ignores a resize to its own size.
func TestAViewerGetsARepaintAfterTheResize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols uint16
		repaints   []string
	}{
		{"another size", 40, 120, []string{"repaint 40x120"}},
		{"the worker's own size", 50, 160, []string{"repaint 50x159", "repaint 50x160"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sv, sock := viewSupervisor(t)
			h := spawn(t, sv, "P3", nil, "FAKE_MODES=1")
			w := watch(t, sock, "P3", tc.rows, tc.cols)
			last := tc.repaints[len(tc.repaints)-1]
			w.waitScreen(last)
			got := w.screen.String()
			preamble := "\x1b[?1004h\x1b[?2004h\x1b[?25l\x1b[H\x1b[2J"
			if !strings.HasPrefix(got, preamble) {
				t.Fatalf("the screen began %q, want %q", got, preamble)
			}
			at := len(preamble)
			for _, r := range tc.repaints {
				i := strings.Index(got[at:], r)
				if i < 0 {
					t.Fatalf("no %q after the preamble, in order: %q", r, got)
				}
				at += i + len(r)
			}
			if strings.Contains(got, "fake-claude ready") {
				t.Errorf("the viewer got output from before it attached: %q", got)
			}
			if r, c := h.s.size(); r != tc.rows || c != tc.cols {
				t.Errorf("the worker's terminal is %dx%d, want %dx%d", r, c, tc.rows, tc.cols)
			}
		})
	}
}

// What the human types in the viewer goes to the worker as the human's: the worker's typing, draft and
// dialog gates see it as they see the lead's human. Terminal reports are not keystrokes.
func TestViewerKeysAreTheHumans(t *testing.T) {
	sv, sock := viewSupervisor(t)
	h := spawn(t, sv, "P3", nil)
	w := watch(t, sock, "P3", 40, 120)
	w.waitScreen("repaint 40x120")

	w.keys.Write([]byte("\x1b[I")) // focus in: the viewer's terminal reports, nobody typed
	waitFor(t, "the report reached claude", func() bool { return strings.Contains(h.input(), "\x1b[I") })
	if v := h.s.View(); !v.LastHumanKey.Equal(time.Unix(0, 0)) || v.Draft {
		t.Fatalf("a terminal report counted as typing: %+v", v)
	}

	before := time.Now()
	w.keys.Write([]byte("abc"))
	waitFor(t, "the keys reached claude", func() bool { return strings.HasSuffix(h.input(), "abc") })
	v := h.s.View()
	if v.LastHumanKey.Before(before) || !v.Draft || v.DraftText != "abc" {
		t.Errorf("the viewer's keys are not the human's: %+v", v)
	}
	w.waitScreen(`got:"abc"`)
}

// Ctrl-] detaches, however the viewer's terminal reports it; keys before it still go to the worker.
// Detached, or gone, the viewer gives the worker's terminal its own size back, and the next viewer can
// attach.
func TestDetachingRestoresTheSize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  string // "" closes the viewer's connection instead
		typed string // what reaches claude
		why   string
	}{
		{"Ctrl-]", "xy\x1d", "xy", ""},
		{"Ctrl-] in the kitty keyboard protocol", "\x1b[93;5u", "", ""},
		{"Ctrl-] in modifyOtherKeys", "z\x1b[27;5;93~", "z", ""},
		{"the viewer's keyboard closes", "\x00", "", ""},
		{"the viewer vanishes", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sv, sock := viewSupervisor(t)
			h := spawn(t, sv, "P3", nil)
			w := watch(t, sock, "P3", 40, 120)
			w.waitScreen("repaint 40x120")
			switch tc.keys {
			case "":
				w.v.conn.Close()
				<-w.done
			case "\x00":
				w.keys.Close()
				if why := w.wait(); why != tc.why {
					t.Errorf("detached because %q", why)
				}
			default:
				w.keys.Write([]byte(tc.keys))
				if why := w.wait(); why != tc.why {
					t.Errorf("detached because %q", why)
				}
			}
			h.waitTail("repaint 50x160")
			if r, c := h.s.size(); r != 50 || c != 160 {
				t.Errorf("the worker's terminal is %dx%d after the viewer left", r, c)
			}
			if tc.typed != "" {
				waitFor(t, "the keys before Ctrl-] reached claude", func() bool { return h.input() == tc.typed })
			}
			time.Sleep(50 * time.Millisecond)
			if got := h.input(); got != tc.typed {
				t.Errorf("claude got %q, want %q", got, tc.typed)
			}
			next := watch(t, sock, "P3", 30, 100)
			next.waitScreen("repaint 30x100")
		})
	}
}

func TestViewRefuses(t *testing.T) {
	sv, sock := viewSupervisor(t)
	spawn(t, sv, "P3", nil)
	ended := spawn(t, sv, "P4", nil)
	ended.s.End()
	ended.wait()
	watch(t, sock, "P3", 40, 120).waitScreen("repaint 40x120")
	for _, tc := range []struct {
		name, session, want string
	}{
		{"an unknown phase", "P9", "no worker on P9 runs in this baton"},
		{"a second viewer", "P3", "P3 already has a viewer: one at a time"},
		{"a worker that has ended", "P4", "P4 has ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Dial(sock, tc.session, 40, 120)
			if err == nil {
				v.conn.Close()
				t.Fatal("attached")
			}
			if err.Error() != tc.want {
				t.Errorf("refused with %q, want %q", err, tc.want)
			}
		})
	}
}

// A viewer is told when the worker it shows ends.
func TestAViewerIsDetachedWhenItsWorkerEnds(t *testing.T) {
	sv, sock := viewSupervisor(t)
	h := spawn(t, sv, "P3", nil)
	w := watch(t, sock, "P3", 40, 120)
	w.waitScreen("repaint 40x120")
	h.s.End()
	if why := w.wait(); why != "P3 ended (exit code 0)" {
		t.Errorf("detached because %q", why)
	}
}

// A viewer that stops reading is detached rather than holding up the worker it shows: the worker's output
// is queued for it, and once the queue is full the viewer is let go, told why after what was queued.
func TestASlowViewerNeverHoldsUpItsWorker(t *testing.T) {
	host, viewerEnd := net.Pipe() // unbuffered: nothing written gets anywhere until the viewer reads
	defer viewerEnd.Close()
	s := &Session{Name: "P3", exited: make(chan struct{}), rows: 50, cols: 160, ownRows: 50, ownCols: 160}
	v := newViewer(host)
	s.viewer = v
	for i := range viewBacklog + 2 {
		s.viewMu.Lock()
		s.toViewer([]byte{byte(i)})
		s.viewMu.Unlock()
	}
	if s.viewing(v) {
		t.Fatal("a viewer that reads nothing is still attached")
	}
	n := 0
	for {
		typ, p, err := readFrame(viewerEnd)
		if err != nil {
			t.Fatalf("after %d frames: %v", n, err)
		}
		if typ == frameDetach {
			if string(p) != "baton: the viewer fell too far behind" || n < viewBacklog {
				t.Errorf("detached after %d frames because %q", n, p)
			}
			return
		}
		n++
	}
}

// Viewers attach through a socket in a directory only the user can enter, which is there only while the
// host runs hidden sessions: a host with no workers is as it was.
func TestTheViewSocketIsPrivateAndGoesWithTheHost(t *testing.T) {
	sock := viewSocket(t)
	r := startRig(t, func(c *Config) { c.ViewSocket = sock })
	time.Sleep(3 * TickInterval)
	if _, err := os.Stat(filepath.Dir(sock)); err == nil {
		t.Fatal("a host with no hidden session listens for viewers")
	}
	spawn(t, r.sv, "P3", nil)
	fi, err := os.Stat(filepath.Dir(sock))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the socket's directory: %v, %v", fi, err)
	}
	r.quit()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the host: %v", err)
	}
}

// baton view's own end: in a real terminal, the worker's screen shows at the terminal's size, keys reach
// the worker, and Ctrl-] leaves with the terminal taken out of the modes claude put it in.
func TestWatchShowsAWorkerInATerminal(t *testing.T) {
	sv, sock := viewSupervisor(t)
	h := spawn(t, sv, "P3", nil, "FAKE_MODES=1")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100})
	screen := &syncBuf{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			screen.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	type result struct {
		why string
		err error
	}
	done := make(chan result, 1)
	go func() {
		why, err := Watch(sock, "P3", tty, tty)
		done <- result{why, err}
	}()
	waitFor(t, "the repaint at the terminal's size", func() bool { return strings.Contains(screen.String(), "repaint 30x100") })
	ptmx.Write([]byte("hi"))
	waitFor(t, "the keys reached the worker", func() bool { return h.input() == "hi" })
	ptmx.Write([]byte{0x1d})
	select {
	case r := <-done:
		if r.why != "" || r.err != nil {
			t.Errorf("Watch returned %q, %v", r.why, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-] did not leave")
	}
	waitFor(t, "the terminal was reset", func() bool { return strings.HasSuffix(screen.String(), viewerReset+"\x1b[999;1H\r\n") })
	h.waitTail("repaint 50x160")
	if _, err := Watch(sock, "P3", h.outFile(t), tty); err != ErrNoTerminal {
		t.Errorf("Watch without a terminal: %v", err)
	}
}

func (h *hidden) outFile(t *testing.T) *os.File {
	f, err := os.Open(filepath.Join(h.out, "input.bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestDetachKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys string
		want int
	}{
		{"none", "abc\r", -1},
		{"plain", "ab\x1dcd", 2},
		{"kitty", "a\x1b[93;5u", 1},
		{"kitty, a press", "\x1b[93;5:1u", 0},
		{"kitty, a release", "\x1b[93;5:3u", -1},
		{"kitty, with alternate keys and text", "\x1b[93:125;5;29u", 0},
		{"kitty, with Num Lock on", "\x1b[93;133u", 0},
		{"kitty, Ctrl+Shift", "\x1b[93;6u", -1},
		{"kitty, ] alone", "\x1b[93u", -1},
		{"kitty, another key with Ctrl", "\x1b[99;5u", -1},
		{"modifyOtherKeys", "\x1b[27;5;93~", 0},
		{"modifyOtherKeys, another key", "\x1b[27;5;99~", -1},
		{"after other sequences", "\x1b[A\x1b[200~x\x1b[201~\x1b[93;5u", 16},
		{"a sequence cut short", "\x1b[93;5", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := detachKey([]byte(tc.keys)); got != tc.want {
				t.Errorf("detachKey(%q) = %d, want %d", tc.keys, got, tc.want)
			}
		})
	}
}
