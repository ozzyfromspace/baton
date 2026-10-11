// Package host runs claude inside a pseudo-terminal that baton owns, passing every byte between the
// user's terminal and claude untouched. Owning the terminal is what lets baton type /compact itself:
// in an interactive Claude Code session, only a keystroke can start a compaction (see
// docs/research/spikes.md). Beside that session, the lead, a host can run hidden ones (Supervisor.Spawn):
// workers whose screens nobody sees, which baton drains and types into the same way.
package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// TickInterval is how often the controller runs.
const TickInterval = 250 * time.Millisecond

// Config describes a host: its lead session, and what its hidden sessions share with it (Spec says the
// rest).
type Config struct {
	Claude   string   // claude executable
	Args     []string // the user's claude arguments
	BatonBin string   // absolute path of the baton binary hooks should call
	Project  *state.Project
	Instance string
	// Session is the session id claude runs under, when baton knows it at launch: one it chose for a new
	// session (--session-id), or one being resumed. Otherwise ("") the session's first hook binds it.
	Session     string
	Version     string
	Autocompact string // passed as --autocompact when non-empty, e.g. "810k"
	Stdin       *os.File
	Stdout      *os.File
	Env         []string // the child's base environment; any BATON_* in it is dropped
	// BatonEnv is baton's own environment for the session it drives: the context valves' settings, a
	// pending command after elevation.
	BatonEnv   []string
	Now        func() time.Time
	Logf       func(format string, a ...any)
	Controller Controller // nil: pure passthrough
	// TypeDelay and EnterDelay pace injected keystrokes so Claude Code reads them as typing, not a paste.
	TypeDelay, EnterDelay time.Duration
	// HangupGrace is how long claude gets to exit after each step of ending it (5s).
	HangupGrace time.Duration
}

// View is what a session's controller knows about it on each tick.
type View struct {
	Now          time.Time
	LastOutput   time.Time // last byte claude wrote to the screen
	LastHumanKey time.Time // last keystroke from the human (terminal reports excluded)
	Draft        bool      // the human typed since their last Enter: the input box may hold their text
	DraftText    string    // that draft as typed, so baton can save it before clearing the box
	// Store is the run the session drives (nil until it is bound to one), and Owner says it does.
	Store *state.Store
	Owner bool
}

// Controller decides, on every tick, whether baton should act.
type Controller interface {
	Tick(v View, in Injector)
}

// Injector types into claude as if at the keyboard.
type Injector interface {
	Type(text string, enter bool) error
	// ClearInput empties Claude Code's input box (Ctrl-C) and forgets the draft baton was tracking.
	// It is how a draft stops being able to hold a run: baton saves the text first, then clears.
	ClearInput() error
	// Quit ends claude (SIGTERM, then SIGKILL with the processes it started if it is still there
	// HangupGrace later): the human is leaving baton, and the shell resumes the conversation as plain
	// Claude Code; or baton restarts the session on a newer version of itself.
	Quit() error
}

// Defaults for a Config's zero values.
const (
	defaultTypeDelay   = 30 * time.Millisecond
	defaultEnterDelay  = 400 * time.Millisecond
	defaultHangupGrace = 5 * time.Second
)

// withoutBatonVars copies env with every BATON_* variable dropped.
func withoutBatonVars(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "BATON_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// bind binds the session baton knows at launch to its run, so the host drives it from the first tick.
// If another baton terminal drives that run (the same conversation resumed twice), this one says so: its
// hooks stay out of the run until that terminal goes away, and then take it over.
func bind(cfg Config) {
	if cfg.Project == nil || cfg.Session == "" {
		return
	}
	s, err := cfg.Project.Bind(state.Binding{Session: cfg.Session, Instance: cfg.Instance})
	if err != nil {
		cfg.Logf("host: binding session %s: %v", cfg.Session, err)
		return
	}
	if st, err := s.Load(); err == nil && !st.IsOwner(cfg.Instance, cfg.Now()) && st.Owner != nil {
		fmt.Fprintf(cfg.Stdout, "baton: another baton terminal (pid %d, since %s) is running this conversation's plan; this one leaves it alone until that one exits.\r\n",
			st.Owner.PID, st.Owner.Started.Local().Format("15:04"))
		cfg.Logf("host: run %s is driven by %s", s.Ref, st.Owner.Instance)
	}
}

// Release lets go of the run the host drives, if any, once claude has exited.
func Release(p *state.Project, instance string) {
	if p == nil {
		return
	}
	if s, ok := p.Lookup("", instance); ok {
		s.Event("host_stopped", nil)
	}
	p.Unbind(instance)
}

func exitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		return signalExitCode(ee)
	}
	return 1
}
