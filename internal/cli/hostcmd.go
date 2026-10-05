package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/statusline"
	"github.com/ozzyfromspace/baton/internal/valve"
	"github.com/ozzyfromspace/baton/internal/version"
)

func init() {
	register("statusline", "print the status line segment (called by Claude Code's status line)", cmdStatusline)
}

// runHost starts claude inside baton's pseudo-terminal in the current directory.
func runHost(args []string, io IO) int { return runHostWith(args, io, nil) }

// runHostWith is runHost with extra environment for the session (e.g. BATON_PENDING after elevation).
func runHostWith(args []string, io IO, extraEnv []string) int {
	if io.Env("BATON_HOST") == "1" {
		return fail(io, "this is already a baton-hosted session; run claude's own commands here instead")
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(io, "cannot find the baton executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(io, "%v", err)
	}
	// An inherited BATON_DIR belongs to some other session; locate this directory's own state.
	dir := state.Locate(cwd, func(k string) string {
		if k == "BATON_DIR" {
			return ""
		}
		return io.Env(k)
	})
	instance := newInstanceID()
	st, err := state.Open(dir, instance, io.Now)
	if err != nil {
		return fail(io, "cannot create %s: %v", dir, err)
	}
	state.ExcludeFromGit(dir)
	logf := fileLogger(filepath.Join(dir, "baton.log"), instance, io.Now)

	claude := io.Env("BATON_CLAUDE")
	if claude == "" {
		claude = "claude"
	}
	cfg := config.Load(batonRoot(io), io.Env)
	autocompact, valves, err := contextSettings(cfg, args, io.Env)
	if err != nil {
		return fail(io, "%v (set it in %s, or BATON_AUTOCOMPACT)", err, config.Path(batonRoot(io)))
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_") {
			env = append(env, kv)
		}
	}
	env = append(env, valves.Env()...)
	env = append(env, extraEnv...)
	timing := loop.DefaultTiming
	// Shorter watchdog timings, for tests and impatient humans.
	for name, field := range map[string]*time.Duration{"BATON_IDLE_NUDGE": &timing.IdleNudge, "BATON_WAIT_GRACE": &timing.WaitGrace, "BATON_WARN_TIMEOUT": &timing.WarnTimeout} {
		if d, err := time.ParseDuration(io.Env(name)); err == nil && d > 0 {
			*field = d
		}
	}
	controller := &loop.Loop{
		Store: st, Notify: notify.New(cfg), Project: filepath.Base(filepath.Dir(dir)),
		Timing: timing, Logf: logf,
	}
	code, err := host.Run(host.Config{
		Claude: claude, Args: args, BatonBin: exe, Store: st, Instance: instance, Version: version.Version,
		Autocompact: autocompact, Stdin: os.Stdin, Stdout: os.Stdout, Env: env, Now: io.Now, Logf: logf,
		Controller: controller,
	})
	if err != nil {
		return fail(io, "%v", err)
	}
	if code != 0 && code != 130 && code != 143 { // not a deliberate Ctrl-C or termination
		st.Update(func(s *state.State) error {
			if s.Mode == state.ModeRunning && s.Run.Ended == nil {
				s.Run.Notices = append(s.Run.Notices, state.Notice{Kind: "session_ended", Text: fmt.Sprintf("claude exited with code %d mid-plan", code), At: io.Now()})
			}
			return nil
		})
	}
	controller.Flush()
	return code
}

// cmdStatusline renders the status line of a hosted session. It must always print something and never
// fail: a broken status line is worse than a plain one.
func cmdStatusline(_ []string, io IO) int {
	raw, _ := goio.ReadAll(goio.LimitReader(io.In, 1<<20))
	in := statusline.Parse(raw)
	var st state.State
	var pl plan.Plan
	havePlan := false
	if dir := io.Env("BATON_DIR"); dir != "" && io.Env("BATON_STATUSLINE_NESTED") == "" {
		if s, err := state.Open(dir, io.Env("BATON_INSTANCE"), io.Now); err == nil {
			st, _ = recordContext(s, in, io)
			if p, err := s.LoadPlan(); err == nil {
				pl, havePlan = p, true
			}
		}
	}
	projectDir := in.Workspace.ProjectDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	home := homeDir(io)
	user := ""
	if cmd := statusline.UserCommand(projectDir, home); cmd != "" {
		user = statusline.RunUser(cmd, raw, projectDir, 3*time.Second)
	}
	fmt.Fprint(io.Out, statusline.Compose(statusline.Segment(st, pl, havePlan), user))
	return 0
}

// contextSettings works out the --autocompact value baton launches claude with, and the context valve
// settings its hooks share. A cap on the command line wins over baton's config, and baton then adds
// none of its own (claude checks that value itself). So does CLAUDE_CODE_AUTO_COMPACT_WINDOW, which
// Claude Code reads before either.
func contextSettings(cfg config.Config, args []string, env func(string) string) (autocompact string, s valve.Settings, err error) {
	s = valve.Settings{CheckpointPct: *cfg.CheckpointPct, WarnPct: *cfg.WarnPct}
	if n, err := strconv.Atoi(env("BATON_WARN_TOKENS")); err == nil && n > 0 {
		s.WarnTokens = n
	}
	autocompact = cfg.Autocompact
	value := autocompact
	if v, ok := flagValue(args, "--autocompact"); ok {
		autocompact, value = "", v
	}
	if value != "" {
		n, ok := config.AutocompactTokens(value)
		if !ok && autocompact != "" {
			return "", s, fmt.Errorf("autocompact %q is not valid: use off, auto, or a size from 100k to 1m", value)
		}
		s.Cap = n
	}
	if n, err := strconv.Atoi(env("CLAUDE_CODE_AUTO_COMPACT_WINDOW")); err == nil && n > 0 {
		s.Cap = min(max(n, config.MinAutocompact), config.MaxAutocompact)
	}
	return autocompact, s, nil
}

// flagValue finds a flag's value in claude's arguments, as "--flag value" or "--flag=value".
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// recordContext stores the context size Claude Code reports, for the context valves. The status line
// refreshes often, so it only writes when the size changed or the reading is 30s old.
func recordContext(s *state.Store, in statusline.Input, io IO) (state.State, error) {
	st, err := s.Load()
	tokens, ok := in.Tokens()
	if err != nil || !ok || !st.IsOwner(io.Env("BATON_INSTANCE"), io.Now()) {
		return st, err
	}
	size, now := in.ContextWindow.ContextWindowSize, io.Now()
	var pct float64
	if p := in.ContextWindow.UsedPercentage; p != nil {
		pct = *p
	}
	limit := valve.FromEnv(io.Env).Limits(size).Window
	if c := st.Run.Context; c != nil && c.Tokens == tokens && c.WindowSize == size && c.Limit == limit && now.Sub(c.At) < 30*time.Second {
		return st, nil
	}
	return s.Update(func(st *state.State) error {
		st.Run.Context = &state.ContextUse{UsedPct: pct, Tokens: tokens, WindowSize: size, Limit: limit, At: now}
		return nil
	})
}

func newInstanceID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fileLogger appends timestamped lines to path; logging must never break the session, so errors are dropped.
func fileLogger(path, instance string, now func() time.Time) func(string, ...any) {
	return func(format string, a ...any) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "%s [%s] %s\n", now().Format("2006-01-02 15:04:05.000"), instance, fmt.Sprintf(format, a...))
		f.Close()
	}
}

// batonRoot is baton's own directory: $BATON_HOME, or ~/.baton.
func batonRoot(io IO) string {
	if r := io.Env("BATON_HOME"); r != "" {
		return r
	}
	return filepath.Join(homeDir(io), ".baton")
}

// homeDir prefers the injected HOME so tests never touch the developer's real settings.
func homeDir(io IO) string {
	if h := io.Env("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}
