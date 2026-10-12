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

// Session is the claude that baton runs in a pseudo-terminal of its own, the lead, whose screen is the
// human's terminal: baton passes every byte it writes through, follows the keys typed into it, types into
// it (it is an Injector), and reaps it when it exits. A Controller, if it has one, is ticked with the
// session's View.
type Session struct {
	cfg Config
	pty pty.PTY
	cmd *exec.Cmd

	screen io.Writer // the human's terminal
	// sizeMu guards closing the terminal, which a resize must not race.
	sizeMu    sync.Mutex
	ptyClosed bool

	exited     chan struct{} // closed once claude has exited
	done       chan struct{} // closed once the session has wound down (Done)
	code       int           // its exit code, once done
	waitErr    error
	hangupOnce sync.Once
	quitOnce   sync.Once
	endedBy    atomic.Pointer[string] // what ended claude, when baton did (EndedBy)

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

// startSession starts cmd in a pseudo-terminal of rows x cols, its screen the human's terminal
// (cfg.Stdout), and reaps it when it exits.
func startSession(cfg Config, cmd *exec.Cmd, rows, cols uint16) (*Session, error) {
	p, err := pty.Start(cmd, rows, cols)
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", cfg.Claude, err)
	}
	s := &Session{cfg: cfg, pty: p, cmd: cmd, screen: cfg.Stdout, exited: make(chan struct{}), done: make(chan struct{})}
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
	close(s.done)
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

// How a session ended (Session.EndedBy).
const (
	EndedSelf   = "self"   // claude exited by itself
	EndedHangup = "hangup" // its terminal went away: baton got SIGHUP, or reading or writing the terminal failed
	EndedSignal = "signal" // baton passed on a termination signal it got (SIGTERM, SIGINT, SIGQUIT)
	EndedQuit   = "quit"   // baton ended it (Quit): a restart, or the human leaving baton
)

// EndedBy says what ended the session, once Done is closed: EndedSelf unless baton ended claude first,
// and then how.
func (s *Session) EndedBy() string {
	if by := s.endedBy.Load(); by != nil {
		return *by
	}
	return EndedSelf
}

// endingBy records what is ending claude, unless something already is, or claude has exited already.
func (s *Session) endingBy(by string) {
	if !s.ended() {
		s.endedBy.CompareAndSwap(nil, &by)
	}
}

// pass passes on a termination signal baton got.
func (s *Session) pass(sig os.Signal) {
	s.endingBy(EndedSignal)
	s.cmd.Process.Signal(sig)
}

// Pid is claude's process id.
func (s *Session) Pid() int { return s.cmd.Process.Pid }

// Instance is the BATON_INSTANCE the session runs as.
func (s *Session) Instance() string { return s.cfg.Instance }

// Resize sets the size of the session's terminal.
func (s *Session) Resize(rows, cols uint16) error {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if s.ptyClosed {
		return os.ErrClosed
	}
	return s.pty.Resize(rows, cols)
}

func (s *Session) logf(format string, a ...any) {
	s.cfg.Logf("host: "+format, a...)
}

// pumpOutput reads everything claude writes, for the human's terminal. If that terminal is gone it keeps
// reading and discards: claude blocks on output nobody reads (a write, or a tcsetattr that waits for the
// output to drain), and a claude blocked there never gets to the hangup it is sent.
func (s *Session) pumpOutput() {
	buf := make([]byte, 32*1024)
	out := s.screen
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.lastOutput.Store(s.cfg.Now().UnixNano())
			if _, werr := out.Write(buf[:n]); werr != nil && out != io.Discard {
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
		s.endingBy(EndedQuit)
		s.logf("quitting %s", s.cfg.Claude)
		go s.sendSignals(quitSignals, false)
	})
	return nil
}

// hangUp ends a session whose terminal has gone (a closed tab, a killed shell): nobody can see it or
// type into it any more. claude gets what a closed terminal would give it, SIGHUP, then SIGTERM and
// SIGKILL if it is still there HangupGrace after each, so it never outlives its terminal. Until then
// it would go on holding the project, and the next baton there would run as plain claude.
func (s *Session) hangUp(why string) {
	s.hangupOnce.Do(func() {
		s.endingBy(EndedHangup)
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
