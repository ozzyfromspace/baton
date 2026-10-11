package host

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/pty"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Session is one claude that baton runs in a pseudo-terminal of its own: the lead, whose screen is the
// human's terminal, or a hidden session (a worker), whose screen nobody sees unless a viewer attaches.
// Either way baton reads every byte it writes, follows the keys typed into it, types into it (it is an
// Injector), and reaps it when it exits. A Controller, if it has one, is ticked with the session's own
// View.
type Session struct {
	// Name is "" for the lead, and a hidden session's name (its phase) otherwise.
	Name string
	cfg  Config // the supervisor's, with this session's Instance and Controller
	pty  pty.PTY
	cmd  *exec.Cmd

	// screen takes the lead's output: the human's terminal. A hidden session's goes to tail and modes,
	// and to its viewer while one is attached (baton view).
	screen io.Writer
	tail   *ring
	modes  *modeTracker
	dump   string // a hidden session's tail is added to this file when it ends ("": none)
	viewMu sync.Mutex
	viewer *viewer
	// sizeMu guards rows and cols, the size of its terminal now, and closing the terminal, which a
	// resize must not race; a hidden session's own size, which it has with no viewer, is ownRows x
	// ownCols.
	sizeMu           sync.Mutex
	rows, cols       uint16
	ptyClosed        bool
	ownRows, ownCols uint16

	exited     chan struct{} // closed once claude has exited
	done       chan struct{} // closed once the session has wound down (Done)
	code       int           // its exit code, once done
	waitErr    error
	hangupOnce sync.Once
	quitOnce   sync.Once
	endOnce    sync.Once

	mu           sync.Mutex // serializes writes to the pty: keys typed by a human vs. baton's typing
	lastOutput   atomic.Int64
	lastHumanKey atomic.Int64
	draft        atomic.Bool
	draftText    atomic.Value // string
	keysMu       sync.Mutex   // keys is fed by Keys and reset by ClearInput
	keys         keyTracker
	lastPanic    time.Time // controller goroutine only
	runMu        sync.Mutex
	run          *state.Store // the run last bound to this session's instance, cached by runOf
}

// startSession starts cmd in a pseudo-terminal of rows x cols, and reaps it when it exits. screen takes
// what it writes; nil makes it a hidden session, whose tail goes into the file dump when it ends ("":
// nowhere).
func startSession(cfg Config, name string, cmd *exec.Cmd, rows, cols uint16, screen io.Writer, dump string) (*Session, error) {
	p, err := pty.Start(cmd, rows, cols)
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", cfg.Claude, err)
	}
	s := &Session{Name: name, cfg: cfg, pty: p, cmd: cmd, screen: screen, dump: dump, exited: make(chan struct{}), done: make(chan struct{}),
		rows: rows, cols: cols, ownRows: rows, ownCols: cols}
	if screen == nil {
		s.tail, s.modes = newRing(TailSize), &modeTracker{}
	}
	s.lastOutput.Store(cfg.Now().UnixNano())
	s.logf("started %s (pid %d), session %q", cfg.Claude, cmd.Process.Pid, cfg.Session)
	go s.reap()
	return s, nil
}

// reap waits for claude to exit, then winds the session down: its controller and heartbeat stop, and its
// last output is read before the pseudo-terminal closes.
func (s *Session) reap() {
	outDone := make(chan struct{})
	go func() { defer close(outDone); s.pumpOutput() }()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.loop(stop) }()
	go func() { defer wg.Done(); s.heartbeat(stop) }()

	s.waitErr = s.cmd.Wait()
	close(s.exited)
	close(stop)
	wg.Wait()
	select { // let the last of claude's output reach the screen
	case <-outDone:
	case <-time.After(time.Second):
	}
	s.sizeMu.Lock()
	s.pty.Close()
	s.ptyClosed = true
	s.sizeMu.Unlock()
	s.code = exitCode(s.cmd, s.waitErr)
	s.logf("%s exited with %d", s.cfg.Claude, s.code)
	s.endViewer()
	s.dumpScreen()
	close(s.done)
}

// dumpScreen adds the latest of a hidden session's screen to its dump file, if it has one, after a line
// saying which session it was and how it ended. It is for debugging only (BATON_SCREEN_DUMP=1).
func (s *Session) dumpScreen() {
	if s.dump == "" || s.tail == nil {
		return
	}
	f, err := os.OpenFile(s.dump, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.logf("saving its screen: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n--- baton: the screen of %s (pid %d), which exited with %d at %s ---\n", s.Name, s.Pid(), s.code, s.cfg.Now().Format(time.RFC3339))
	f.Write(s.tail.Bytes())
}

// Done is closed once the session has ended and wound down; ExitCode is then claude's exit code.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitCode is claude's exit code, once Done is closed (-1 before).
func (s *Session) ExitCode() int {
	select {
	case <-s.done:
		return s.code
	default:
		return -1
	}
}

// Pid is claude's process id.
func (s *Session) Pid() int { return s.cmd.Process.Pid }

// Instance is the BATON_INSTANCE the session runs as.
func (s *Session) Instance() string { return s.cfg.Instance }

// Hidden reports a session whose screen nobody sees.
func (s *Session) Hidden() bool { return s.screen == nil }

// Tail is the latest of a hidden session's output, up to TailSize bytes, oldest first.
func (s *Session) Tail() []byte {
	if s.tail == nil {
		return nil
	}
	return s.tail.Bytes()
}

// Modes is what puts a terminal into the private modes a hidden session's output has set, each in its
// latest state, for a viewer about to show its screen.
func (s *Session) Modes() []byte {
	if s.modes == nil {
		return nil
	}
	return s.modes.sequences()
}

// Resize sets the size of the session's terminal.
func (s *Session) Resize(rows, cols uint16) error {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if s.ptyClosed {
		return os.ErrClosed
	}
	if err := s.pty.Resize(rows, cols); err != nil {
		return err
	}
	s.rows, s.cols = rows, cols
	return nil
}

// size is the size of the session's terminal.
func (s *Session) size() (rows, cols uint16) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	return s.rows, s.cols
}

func (s *Session) logf(format string, a ...any) {
	if s.Name != "" {
		format = s.Name + ": " + format
	}
	s.cfg.Logf("host: "+format, a...)
}

// pumpOutput reads everything claude writes. The lead's goes to the human's terminal. If that terminal is
// gone it keeps reading and discards: claude blocks on output nobody reads (a write, or a tcsetattr that
// waits for the output to drain), and a claude blocked there never gets to the hangup it is sent. A
// hidden session's output is kept in its tail and queued for its viewer, so it never blocks either.
func (s *Session) pumpOutput() {
	buf := make([]byte, 32*1024)
	out := s.screen
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.lastOutput.Store(s.cfg.Now().UnixNano())
			if out == nil {
				s.viewMu.Lock()
				s.tail.Write(buf[:n])
				s.modes.feed(buf[:n])
				s.toViewer(buf[:n])
				s.viewMu.Unlock()
			} else if _, werr := out.Write(buf[:n]); werr != nil && out != io.Discard {
				out = io.Discard
				s.hangUp(fmt.Sprintf("writing to it failed: %v", werr))
			}
		}
		if err != nil {
			return
		}
	}
}

// Keys passes keys a human typed to claude, and follows them: whether someone is at the keyboard, and
// whether the input box may hold their draft.
func (s *Session) Keys(b []byte) error {
	now := s.cfg.Now()
	s.keysMu.Lock()
	human := s.keys.feed(b, now)
	draft, text := s.keys.draft(), s.keys.draftText()
	s.keysMu.Unlock()
	if human {
		s.lastHumanKey.Store(now.UnixNano())
	}
	s.draft.Store(draft)
	s.draftText.Store(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.pty.Write(b)
	return err
}

// Type sends text one character at a time, then (optionally) a lone carriage return after a pause, so
// Claude Code sees typing followed by Enter rather than a paste. It holds the write lock throughout so a
// human keystroke can never land in the middle.
func (s *Session) Type(text string, enter bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logf("typing %q (enter=%v)", text, enter)
	for _, r := range text {
		if _, err := s.pty.Write([]byte(string(r))); err != nil {
			return err
		}
		time.Sleep(s.cfg.TypeDelay)
	}
	if enter {
		time.Sleep(s.cfg.EnterDelay)
		if _, err := s.pty.Write([]byte{'\r'}); err != nil {
			return err
		}
	}
	return nil
}

// ClearInput empties Claude Code's input box and forgets the draft. Ctrl-C is what empties that box (the
// key tracker reads it the same way), and the tracker is reset explicitly because it only ever sees the
// human's keys — it cannot observe baton's own write.
func (s *Session) ClearInput() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logf("clearing the input box")
	if _, err := s.pty.Write([]byte{0x03}); err != nil {
		return err
	}
	s.keysMu.Lock()
	s.keys.clear()
	s.keysMu.Unlock()
	s.draft.Store(false)
	s.draftText.Store("")
	return nil
}

// Quit ends claude once, whatever the controller asks: SIGTERM, then SIGKILL (with the processes it
// started) if it is still there HangupGrace later.
func (s *Session) Quit() error {
	s.quitOnce.Do(func() {
		s.logf("quitting %s", s.cfg.Claude)
		go s.sendSignals(quitSignals, false)
	})
	return nil
}

// End ends the session as a human would, with /exit and Enter (a draft in the input box is cleared
// first), then as Quit does if it is still there HangupGrace later. It returns at once; Done closes once
// the session has ended. With a dialog open in it (its run records one, or cannot be read), nothing is
// typed: keys typed into a permission prompt pick one of its options. It gets SIGTERM instead, as from
// Quit.
func (s *Session) End() error {
	s.endOnce.Do(func() {
		select {
		case <-s.exited:
			return
		default:
		}
		s.logf("ending %s", s.cfg.Claude)
		go func() {
			if s.dialogMayBeOpen() {
				s.logf("a dialog may be open in it: signalling instead of typing /exit")
				s.sendSignals(quitSignals, true)
				return
			}
			if s.draft.Load() {
				s.ClearInput()
			}
			s.Type("/exit", true)
			select {
			case <-s.exited:
				return
			case <-time.After(s.cfg.HangupGrace):
			}
			s.sendSignals(quitSignals, true)
		}()
	})
	return nil
}

// dialogMayBeOpen reports a session whose run records a dialog waiting on the human, or whose run cannot
// be read. A session bound to no run has had no hook run yet, so it has recorded no dialog either.
func (s *Session) dialogMayBeOpen() bool {
	st := s.runOf()
	if st == nil {
		return false
	}
	rs, err := st.Load()
	return err != nil || rs.Run.Dialogs.AnyOpen()
}

// hangUp ends a session whose terminal has gone (a closed tab, a killed shell): nobody can see it or
// type into it any more. claude gets what a closed terminal would give it, SIGHUP, then SIGTERM and
// SIGKILL if it is still there HangupGrace after each, so it never outlives its terminal. Until then
// it would go on holding the project, and the next baton there would run as plain claude.
func (s *Session) hangUp(why string) {
	s.hangupOnce.Do(func() {
		s.logf("the terminal is gone (%s); ending %s", why, s.cfg.Claude)
		if st := s.runOf(); st != nil {
			st.Event("terminal_gone", map[string]any{"why": why})
		}
		go s.sendSignals(hangupSignals, true)
	})
}

// sendSignals sends sigs to claude in order, HangupGrace apart, until it has exited. logged says whether
// each is logged.
func (s *Session) sendSignals(sigs []os.Signal, logged bool) {
	for _, sig := range sigs {
		select {
		case <-s.exited:
			return
		default:
		}
		if logged {
			s.logf("sending %v to %s", sig, s.cfg.Claude)
		}
		if sig == killSignal {
			// claude's own children go too: the Bash tool's shell runs in a process session of its own,
			// and outlives a claude that was killed (spike 23).
			killTree(s.cmd.Process)
		} else {
			s.cmd.Process.Signal(sig)
		}
		select {
		case <-s.exited:
			return
		case <-time.After(s.cfg.HangupGrace):
		}
	}
}

func (s *Session) draftOf() string {
	t, _ := s.draftText.Load().(string)
	return t
}

// View is what the session's controller knows about it now.
func (s *Session) View() View {
	st := s.runOf()
	return View{
		Now:          s.cfg.Now(),
		LastOutput:   time.Unix(0, s.lastOutput.Load()),
		LastHumanKey: time.Unix(0, s.lastHumanKey.Load()),
		Draft:        s.draft.Load(),
		DraftText:    s.draftOf(),
		Store:        st,
		Owner:        st != nil,
	}
}

// runOf returns the run this session's instance drives. The session's hooks bind it
// (state.Project.Bind), so it can change while claude runs: from none to one once the session starts, or
// to another one when the human switches conversations. A run newly bound is logged (host_started).
func (s *Session) runOf() *state.Store {
	if s.cfg.Project == nil {
		return nil
	}
	ref := s.cfg.Project.HostRun(s.cfg.Instance)
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if ref == "" {
		s.run = nil
		return nil
	}
	if s.run != nil && s.run.Ref == ref {
		return s.run
	}
	st, err := s.cfg.Project.Run(ref, s.cfg.Instance)
	if err != nil {
		return nil
	}
	s.run = st
	st.Event("host_started", map[string]any{"pid": os.Getpid(), "version": s.cfg.Version, "git": gitx.Usable(st.Root)})
	s.logf("driving run %s", st.Ref)
	return st
}

// heartbeat keeps this session's claim on its run fresh. It runs on its own, so a controller that is busy
// (typing, or waiting on a slow notification) can never let the claim lapse: a run whose claim lapsed
// can be taken over by another terminal resuming the same conversation.
func (s *Session) heartbeat(stop <-chan struct{}) {
	tick := time.NewTicker(state.OwnerTTL / 6)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		st := s.runOf()
		if st == nil {
			continue
		}
		now := s.cfg.Now()
		if _, err := st.Update(func(rs *state.State) error {
			return state.Claim(rs, s.cfg.Instance, os.Getpid(), now)
		}); err != nil {
			s.logf("heartbeat failed: %v", err)
			if rs, lerr := st.Load(); lerr == nil && !rs.IsOwner(s.cfg.Instance, now) {
				// Another terminal took the run over while this one's claim had lapsed (the machine
				// slept): it drives the run now, and this host lets go.
				s.logf("run %s is now driven by %s; letting go", st.Ref, rs.Owner.Instance)
				s.cfg.Project.Unbind(s.cfg.Instance)
			}
		}
	}
}

func (s *Session) loop(stop <-chan struct{}) {
	tick := time.NewTicker(TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		v := s.View()
		if s.cfg.Controller != nil {
			s.tick(v)
		}
	}
}

// tick runs the controller once. A bug in it must not take the session down with it (the human would
// lose claude mid-plan), so a panic is logged and the next tick runs as usual.
func (s *Session) tick(v View) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("controller panic: %v", r)
			if v.Store != nil && v.Now.Sub(s.lastPanic) > time.Minute {
				s.lastPanic = v.Now
				v.Store.Event("controller_panic", map[string]any{"error": fmt.Sprint(r)})
			}
		}
	}()
	s.cfg.Controller.Tick(v, s)
}
