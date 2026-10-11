package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/term"
)

// Supervisor runs the sessions of one baton host: the lead, in the human's terminal, and hidden sessions
// beside it (Spawn). The lead alone has the terminal: its raw mode, its keys and its size. Every session
// is reaped on its own, and when the lead ends, so do the hidden sessions.
type Supervisor struct {
	cfg  Config
	lead *Session

	mu     sync.Mutex
	hidden map[string]*Session // by name, the latest session of each
	closed bool                // the lead has ended: no session starts any more
}

// Spec describes a hidden session. Its command line and environment are used as given.
type Spec struct {
	Name     string   // unique among the live hidden sessions: a worker's phase
	Dir      string   // its working directory
	Args     []string // claude's arguments
	Env      []string // its whole environment
	Instance string   // the BATON_INSTANCE it runs as: its run's heartbeat claims the run as this
	// Rows and Cols are the size of its terminal (0: 50 x 160).
	Rows, Cols uint16
	Controller Controller // ticked with the session's own View; nil: none
}

// NewSupervisor makes the supervisor for the session cfg describes, which Run starts.
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.TypeDelay == 0 {
		cfg.TypeDelay = defaultTypeDelay
	}
	if cfg.EnterDelay == 0 {
		cfg.EnterDelay = defaultEnterDelay
	}
	if cfg.HangupGrace == 0 {
		cfg.HangupGrace = defaultHangupGrace
	}
	return &Supervisor{cfg: cfg, hidden: map[string]*Session{}}
}

// Run starts the lead in the human's terminal and returns its exit code once it has exited. The hidden
// sessions end then too (End), and Run waits for them.
func (sv *Supervisor) Run() (int, error) {
	cfg := sv.cfg
	bind(cfg)
	args := append([]string{"--settings", SettingsJSON(cfg.BatonBin)}, cfg.Args...)
	if cfg.Autocompact != "" {
		args = append([]string{"--autocompact", cfg.Autocompact}, args...)
	}
	// Always start from an environment with no BATON_* in it. A session started inside another one
	// inherits them, and hooks that kept them would write the FIRST session's run.
	env := append(withoutBatonVars(cfg.Env), cfg.BatonEnv...)
	env = append(env, "BATON_HOST=1", "BATON_INSTANCE="+cfg.Instance, "BATON_BIN="+cfg.BatonBin, "BATON_VERSION="+cfg.Version)
	if cfg.Project != nil {
		env = append(env, "BATON_DIR="+cfg.Project.Dir)
	}
	cmd := exec.Command(cfg.Claude, args...)
	cmd.Env = env

	rows, cols := uint16(50), uint16(160)
	interactive := term.IsTerminal(int(cfg.Stdin.Fd()))
	if interactive {
		if c, r, err := term.GetSize(int(cfg.Stdout.Fd())); err == nil {
			rows, cols = uint16(r), uint16(c)
		}
	}
	lead, err := startSession(cfg, "", cmd, rows, cols, cfg.Stdout)
	if err != nil {
		sv.close()
		return 1, err
	}
	sv.mu.Lock()
	sv.lead = lead
	sv.mu.Unlock()

	// From here on the user's terminal is raw; restore it on every way out, panics included.
	restore := func() {}
	if interactive {
		if old, err := term.MakeRaw(int(cfg.Stdin.Fd())); err == nil {
			restore = func() { term.Restore(int(cfg.Stdin.Fd()), old) }
			defer restore()
		}
	}
	go sv.pumpInput(lead, interactive)
	stopSignals := forwardSignals(signalHandlers{
		resize: func() {
			if interactive {
				if c, r, err := term.GetSize(int(cfg.Stdout.Fd())); err == nil {
					lead.Resize(uint16(r), uint16(c))
				}
			}
		},
		hangup: func() {
			lead.hangUp("baton got SIGHUP")
			sv.endHidden()
		},
		forward: func(sig os.Signal) {
			lead.cmd.Process.Signal(sig)
			sv.endHidden()
		},
	})
	defer stopSignals()

	<-lead.Done()
	restore()
	sv.close()
	sv.endHidden()
	for _, s := range sv.Hidden() {
		<-s.Done()
	}
	return lead.code, nil
}

// pumpInput passes the human's keys to the lead.
func (sv *Supervisor) pumpInput(lead *Session, interactive bool) {
	buf := make([]byte, 4096)
	for {
		n, err := sv.cfg.Stdin.Read(buf)
		if err != nil && interactive {
			// A terminal in raw mode never reads as EOF (Ctrl-D is a byte): the terminal is gone.
			lead.hangUp(fmt.Sprintf("reading from it failed: %v", err))
		}
		if n > 0 {
			if werr := lead.Keys(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ErrClosed means the supervisor's lead has ended, so no session starts any more.
var ErrClosed = errors.New("the lead session has ended")

// Spawn starts a hidden session. Its output is drained into its tail, it has no terminal of the human's,
// and it ends with the lead.
func (sv *Supervisor) Spawn(spec Spec) (*Session, error) {
	if spec.Name == "" || spec.Instance == "" {
		return nil, errors.New("a hidden session needs a name and an instance")
	}
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.closed {
		return nil, ErrClosed
	}
	if s := sv.hidden[spec.Name]; s != nil && !s.ended() {
		return nil, fmt.Errorf("session %s is running already", spec.Name)
	}
	rows, cols := spec.Rows, spec.Cols
	if rows == 0 || cols == 0 {
		rows, cols = 50, 160
	}
	cmd := exec.Command(sv.cfg.Claude, spec.Args...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	cfg := sv.cfg
	cfg.Instance, cfg.Controller, cfg.Session = spec.Instance, spec.Controller, ""
	s, err := startSession(cfg, spec.Name, cmd, rows, cols, nil)
	if err != nil {
		return nil, err
	}
	sv.hidden[spec.Name] = s
	return s, nil
}

// Lead is the lead session, once Run has started it.
func (sv *Supervisor) Lead() *Session {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.lead
}

// Session is the latest hidden session named name, ended or not, or nil.
func (sv *Supervisor) Session(name string) *Session {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.hidden[name]
}

// Hidden lists the latest hidden session of each name, ended or not.
func (sv *Supervisor) Hidden() []*Session {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	out := make([]*Session, 0, len(sv.hidden))
	for _, s := range sv.hidden {
		out = append(out, s)
	}
	return out
}

func (sv *Supervisor) close() {
	sv.mu.Lock()
	sv.closed = true
	sv.mu.Unlock()
}

// endHidden ends every hidden session still running.
func (sv *Supervisor) endHidden() {
	for _, s := range sv.Hidden() {
		s.End()
	}
}

// ended reports a session whose claude has exited.
func (s *Session) ended() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}
