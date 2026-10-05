// Package host runs claude inside a pseudo-terminal that baton owns, passing every byte between the
// user's terminal and claude untouched. Owning the terminal is what lets baton type /compact itself:
// in an interactive Claude Code session, only a keystroke can start a compaction (see
// docs/research/spikes.md).
package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
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
	Env         []string // the child's base environment; any BATON_* in it is dropped
	// BatonEnv is baton's own environment for the session it drives: the context valves' settings, a
	// pending command after elevation. Only an owner gets it.
	BatonEnv   []string
	Now        func() time.Time
	Logf       func(format string, a ...any)
	Controller Controller // nil: pure passthrough
	// TypeDelay and EnterDelay pace injected keystrokes so Claude Code reads them as typing, not a paste.
	TypeDelay, EnterDelay time.Duration
	// HangupGrace is how long claude gets to exit after each signal once the terminal is gone (5s).
	HangupGrace time.Duration
}

// View is what the controller knows about the terminal on each tick.
type View struct {
	Now          time.Time
	LastOutput   time.Time // last byte claude wrote to the screen
	LastHumanKey time.Time // last keystroke from the human (terminal reports excluded)
	Draft        bool      // the human typed since their last Enter: the input box may hold their text
	DraftText    string    // that draft as typed, so baton can save it before clearing the box
	Owner        bool      // this session drives the project's plan
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
	if cfg.HangupGrace == 0 {
		cfg.HangupGrace = 5 * time.Second
	}

	owner := claim(cfg)
	args := cfg.Args
	// Always start from an environment with no BATON_* in it. A session started inside another one
	// inherits them, and a non-owner that kept them would run hooks pointed at the FIRST session's
	// instance and .baton directory — writing another plan's state from a session that is not driving
	// it. The owner sets its own below.
	env := withoutBatonVars(cfg.Env)
	if owner {
		args = append([]string{"--settings", SettingsJSON(cfg.BatonBin)}, args...)
		if cfg.Autocompact != "" {
			args = append([]string{"--autocompact", cfg.Autocompact}, args...)
		}
		env = append(env, cfg.BatonEnv...)
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

	h := &session{cfg: cfg, pty: p, cmd: cmd, interactive: interactive, exited: make(chan struct{})}
	h.lastOutput.Store(cfg.Now().UnixNano())

	outDone := make(chan struct{})
	go func() { defer close(outDone); h.pumpOutput() }()
	go h.pumpInput()
	stopSignals := forwardSignals(cmd, p, cfg.Stdout, interactive, func() { h.hangUp("baton got SIGHUP") })
	defer stopSignals()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.loop(stop, owner) }()
	go func() { defer wg.Done(); h.heartbeat(stop, owner) }()

	werr := cmd.Wait()
	close(h.exited)
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
	cmd          *exec.Cmd
	interactive  bool          // the user's terminal is a terminal, not a pipe
	exited       chan struct{} // closed once claude has exited
	hangupOnce   sync.Once
	mu           sync.Mutex // serializes writes to the pty: human keystrokes vs. injected typing
	lastOutput   atomic.Int64
	lastHumanKey atomic.Int64
	draft        atomic.Bool
	draftText    atomic.Value // string
	keysMu       sync.Mutex   // keys is fed by pumpInput and reset by the controller
	keys         keyTracker
	lastPanic    time.Time // controller goroutine only
}

// pumpOutput copies claude's screen to the user's terminal. If the terminal is gone it keeps reading and
// discards: claude blocks on output nobody reads (a write, or a tcsetattr that waits for the output to
// drain), and a claude blocked there never gets to the hangup it is sent.
func (h *session) pumpOutput() {
	buf := make([]byte, 32*1024)
	out := io.Writer(h.cfg.Stdout)
	for {
		n, err := h.pty.Read(buf)
		if n > 0 {
			h.lastOutput.Store(h.cfg.Now().UnixNano())
			if _, werr := out.Write(buf[:n]); werr != nil && out != io.Discard {
				out = io.Discard
				h.hangUp(fmt.Sprintf("writing to it failed: %v", werr))
			}
		}
		if err != nil {
			return
		}
	}
}

// hangUp ends a session whose terminal has gone (a closed tab, a killed shell): nobody can see it or
// type into it any more. claude gets what a closed terminal would give it, SIGHUP, then SIGTERM and
// SIGKILL if it is still there HangupGrace after each, so it never outlives its terminal. Until then
// it would go on holding the project, and the next baton there would run as plain claude.
func (h *session) hangUp(why string) {
	h.hangupOnce.Do(func() {
		h.cfg.Logf("host: the terminal is gone (%s); ending %s", why, h.cfg.Claude)
		if h.cfg.Store != nil {
			h.cfg.Store.Event("terminal_gone", map[string]any{"why": why})
		}
		go func() {
			for _, sig := range hangupSignals {
				select {
				case <-h.exited:
					return
				default:
				}
				h.cfg.Logf("host: sending %v to %s", sig, h.cfg.Claude)
				h.cmd.Process.Signal(sig)
				select {
				case <-h.exited:
					return
				case <-time.After(h.cfg.HangupGrace):
				}
			}
		}()
	})
}

func (h *session) pumpInput() {
	buf := make([]byte, 4096)
	for {
		n, err := h.cfg.Stdin.Read(buf)
		if err != nil && h.interactive {
			// A terminal in raw mode never reads as EOF (Ctrl-D is a byte): the terminal is gone.
			h.hangUp(fmt.Sprintf("reading from it failed: %v", err))
		}
		if n > 0 {
			now := h.cfg.Now()
			h.keysMu.Lock()
			human := h.keys.feed(buf[:n], now)
			draft, text := h.keys.draft(), h.keys.draftText()
			h.keysMu.Unlock()
			if human {
				h.lastHumanKey.Store(now.UnixNano())
			}
			h.draft.Store(draft)
			h.draftText.Store(text)
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

func (h *session) draftOf() string {
	t, _ := h.draftText.Load().(string)
	return t
}

// ClearInput empties Claude Code's input box and forgets the draft. Ctrl-C is what empties that box (the
// key tracker reads it the same way), and the tracker is reset explicitly because it only ever sees the
// human's stdin — it cannot observe baton's own write.
func (h *session) ClearInput() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Logf("host: clearing the input box")
	if _, err := h.pty.Write([]byte{0x03}); err != nil {
		return err
	}
	h.keysMu.Lock()
	h.keys.clear()
	h.keysMu.Unlock()
	h.draft.Store(false)
	h.draftText.Store("")
	return nil
}

// heartbeat keeps this session's claim on the project fresh. It runs on its own, so a controller that is
// busy (typing, or waiting on a slow notification) can never let the claim lapse: a lapsed claim makes
// every hook dormant.
func (h *session) heartbeat(stop <-chan struct{}, owner bool) {
	if !owner {
		return
	}
	tick := time.NewTicker(state.OwnerTTL / 6)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		now := h.cfg.Now()
		if _, err := h.cfg.Store.Update(func(st *state.State) error {
			return state.Claim(st, h.cfg.Instance, os.Getpid(), now)
		}); err != nil {
			h.cfg.Logf("host: heartbeat failed: %v", err)
		}
	}
}

func (h *session) loop(stop <-chan struct{}, owner bool) {
	if h.cfg.Controller == nil {
		return
	}
	tick := time.NewTicker(TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		h.tick(View{
			Now:          h.cfg.Now(),
			LastOutput:   time.Unix(0, h.lastOutput.Load()),
			LastHumanKey: time.Unix(0, h.lastHumanKey.Load()),
			Draft:        h.draft.Load(),
			DraftText:    h.draftOf(),
			Owner:        owner,
		})
	}
}

// tick runs the controller once. A bug in it must not take the session down with it (the human would
// lose claude mid-plan), so a panic is logged and the next tick runs as usual.
func (h *session) tick(v View) {
	defer func() {
		if r := recover(); r != nil {
			h.cfg.Logf("host: controller panic: %v", r)
			if h.cfg.Store != nil && v.Now.Sub(h.lastPanic) > time.Minute {
				h.lastPanic = v.Now
				h.cfg.Store.Event("controller_panic", map[string]any{"error": fmt.Sprint(r)})
			}
		}
	}()
	h.cfg.Controller.Tick(v, h)
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
	cfg.Store.Event("host_started", map[string]any{"pid": os.Getpid(), "version": cfg.Version, "git": gitx.Usable(filepath.Dir(cfg.Store.Dir))})
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
