// Package host runs claude inside a pseudo-terminal that baton owns, passing every byte between the
// user's terminal and claude untouched. Owning the terminal is what lets baton type /compact itself:
// in an interactive Claude Code session, only a keystroke can start a compaction (see
// docs/research/spikes.md).
package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/pty"
	"github.com/ozzyfromspace/baton/internal/state"
	"golang.org/x/term"
)

// TickInterval is how often the controller runs.
const TickInterval = 250 * time.Millisecond

// Config describes one hosted session.
type Config struct {
	Claude      string   // claude executable
	Args        []string // the user's claude arguments
	BatonBin    string   // absolute path of the baton binary hooks should call
	Store       *state.Store
	Instance    string
	Version     string
	Autocompact string // passed as --autocompact when non-empty, e.g. "810k"
	Stdin       *os.File
	Stdout      *os.File
	Env         []string // the child's base environment
	Now         func() time.Time
	Logf        func(format string, a ...any)
	Controller  Controller // nil: pure passthrough
	// TypeDelay and EnterDelay pace injected keystrokes so Claude Code reads them as typing, not a paste.
	TypeDelay, EnterDelay time.Duration
}

// View is what the controller knows about the terminal on each tick.
type View struct {
	Now          time.Time
	LastOutput   time.Time // last byte claude wrote to the screen
	LastHumanKey time.Time // last keystroke from the human (terminal reports excluded)
	Draft        bool      // the human typed since their last Enter: the input box may hold their text
	Owner        bool      // this session drives the project's plan
}

// Controller decides, on every tick, whether baton should act.
type Controller interface {
	Tick(v View, in Injector)
}

// Injector types into claude as if at the keyboard.
type Injector interface {
	Type(text string, enter bool) error
}

// Run hosts claude until it exits and returns its exit code.
func Run(cfg Config) (int, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.TypeDelay == 0 {
		cfg.TypeDelay = 30 * time.Millisecond
	}
	if cfg.EnterDelay == 0 {
		cfg.EnterDelay = 400 * time.Millisecond
	}

	owner := claim(cfg)
	args := cfg.Args
	env := append([]string(nil), cfg.Env...)
	if owner {
		args = append([]string{"--settings", SettingsJSON(cfg.BatonBin)}, args...)
		if cfg.Autocompact != "" {
			args = append([]string{"--autocompact", cfg.Autocompact}, args...)
		}
		env = append(env, "BATON_HOST=1", "BATON_DIR="+cfg.Store.Dir, "BATON_INSTANCE="+cfg.Instance,
			"BATON_BIN="+cfg.BatonBin, "BATON_VERSION="+cfg.Version)
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
	p, err := pty.Start(cmd, rows, cols)
	if err != nil {
		release(cfg, owner)
		return 1, fmt.Errorf("starting %s: %w", cfg.Claude, err)
	}
	cfg.Logf("host: started %s (pid %d), owner=%v", cfg.Claude, cmd.Process.Pid, owner)

	// From here on the user's terminal is raw; restore it on every way out, panics included.
	if interactive {
		if old, err := term.MakeRaw(int(cfg.Stdin.Fd())); err == nil {
			defer term.Restore(int(cfg.Stdin.Fd()), old)
		}
	}
	defer release(cfg, owner)

	h := &session{cfg: cfg, pty: p}
	h.lastOutput.Store(cfg.Now().UnixNano())

	outDone := make(chan struct{})
	go func() { defer close(outDone); h.pumpOutput() }()
	go h.pumpInput()
	stopSignals := forwardSignals(cmd, p, cfg.Stdout, interactive)
	defer stopSignals()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.loop(stop, owner) }()

	werr := cmd.Wait()
	close(stop)
	wg.Wait()
	select { // let the last of claude's output reach the screen
	case <-outDone:
	case <-time.After(time.Second):
	}
	p.Close()
	code := exitCode(cmd, werr)
	cfg.Logf("host: %s exited with %d", cfg.Claude, code)
	return code, nil
}

type session struct {
	cfg          Config
	pty          pty.PTY
	mu           sync.Mutex // serializes writes to the pty: human keystrokes vs. injected typing
	lastOutput   atomic.Int64
	lastHumanKey atomic.Int64
	draft        atomic.Bool
	keys         keyTracker
}

func (h *session) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := h.pty.Read(buf)
		if n > 0 {
			h.lastOutput.Store(h.cfg.Now().UnixNano())
			if _, werr := h.cfg.Stdout.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *session) pumpInput() {
	buf := make([]byte, 4096)
	for {
		n, err := h.cfg.Stdin.Read(buf)
		if n > 0 {
			switch h.keys.feed(buf[:n]) {
			case inputTyping:
				h.lastHumanKey.Store(h.cfg.Now().UnixNano())
				h.draft.Store(true)
			case inputSubmit:
				h.lastHumanKey.Store(h.cfg.Now().UnixNano())
				h.draft.Store(false)
			}
			h.mu.Lock()
			_, werr := h.pty.Write(buf[:n])
			h.mu.Unlock()
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Type sends text one character at a time, then (optionally) a lone carriage return after a pause, so
// Claude Code sees typing followed by Enter rather than a paste. It holds the write lock throughout so a
// human keystroke can never land in the middle.
func (h *session) Type(text string, enter bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Logf("host: typing %q (enter=%v)", text, enter)
	for _, r := range text {
		if _, err := h.pty.Write([]byte(string(r))); err != nil {
			return err
		}
		time.Sleep(h.cfg.TypeDelay)
	}
	if enter {
		time.Sleep(h.cfg.EnterDelay)
		if _, err := h.pty.Write([]byte{'\r'}); err != nil {
			return err
		}
	}
	return nil
}

func (h *session) loop(stop <-chan struct{}, owner bool) {
	tick := time.NewTicker(TickInterval)
	defer tick.Stop()
	lastBeat := h.cfg.Now()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		now := h.cfg.Now()
		if owner && now.Sub(lastBeat) >= state.OwnerTTL/3 {
			lastBeat = now
			if _, err := h.cfg.Store.Update(func(st *state.State) error {
				return state.Claim(st, h.cfg.Instance, os.Getpid(), now)
			}); err != nil {
				h.cfg.Logf("host: heartbeat failed: %v", err)
			}
		}
		if h.cfg.Controller == nil {
			continue
		}
		v := View{
			Now:          now,
			LastOutput:   time.Unix(0, h.lastOutput.Load()),
			LastHumanKey: time.Unix(0, h.lastHumanKey.Load()),
			Draft:        h.draft.Load(),
			Owner:        owner,
		}
		h.cfg.Controller.Tick(v, h)
	}
}

// claim makes this session the project's owner, or explains why it runs as a plain passthrough.
func claim(cfg Config) bool {
	if cfg.Store == nil {
		return false
	}
	_, err := cfg.Store.Update(func(st *state.State) error {
		return state.Claim(st, cfg.Instance, os.Getpid(), cfg.Now())
	})
	if err != nil {
		fmt.Fprintf(cfg.Stdout, "baton: %v — this session runs as plain claude (no baton hooks).\r\n", err)
		cfg.Logf("host: not owner: %v", err)
		return false
	}
	cfg.Store.Event("host_started", map[string]any{"pid": os.Getpid(), "version": cfg.Version})
	return true
}

func release(cfg Config, owner bool) {
	if !owner {
		return
	}
	cfg.Store.Update(func(st *state.State) error { state.Release(st, cfg.Instance); return nil })
	cfg.Store.Event("host_stopped", nil)
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
