package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/statusline"
	"github.com/ozzyfromspace/baton/internal/upgrade"
	"github.com/ozzyfromspace/baton/internal/valve"
	"github.com/ozzyfromspace/baton/internal/version"
)

func init() {
	register("statusline", "print the status line segment (called by Claude Code's status line)", cmdStatusline)
}

// HostBinary is the binary a command run inside a hosted session should hand itself to: the one hosting
// the session, when the `baton` that ran is another. After an update, the newest baton comes first on the
// PATH while the host still runs its own version, and a session must stay on one version (its hooks, its
// CLI and its /baton instructions) until the host restarts it. "" means run here.
func HostBinary(env func(string) string) string {
	bin := env("BATON_BIN")
	if env("BATON_HOST") != "1" || bin == "" || !upgrade.CanRestart {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	same := func(a, b string) bool {
		ra, aerr := filepath.EvalSymlinks(a)
		rb, berr := filepath.EvalSymlinks(b)
		return aerr == nil && berr == nil && ra == rb
	}
	if fi, err := os.Stat(bin); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 || same(exe, bin) {
		return ""
	}
	return bin
}

// runHost starts claude inside baton's pseudo-terminal in the current directory. Not from inside a claude
// session: there, a mistyped or retired command name would be taken for a prompt, and another claude
// would start inside the Bash tool.
func runHost(args []string, io IO) int {
	if hosted, inSession := io.Env("BATON_HOST") == "1", io.Env("CLAUDE_CODE_SESSION_ID") != ""; hosted || inSession {
		what := "`baton` starts a new claude session"
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			what = fmt.Sprintf("%q is not a baton command, and `baton <prompt>` starts a new claude session", args[0])
		}
		if hosted {
			return fail(io, "%s; this is already one, hosted by baton. `baton help` lists baton's commands.", what)
		}
		return fail(io, "%s; this is already one. To hand it to baton, run /baton start; `baton help` lists baton's commands.", what)
	}
	return runHostWith(args, io, nil)
}

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
	proj, err := state.OpenProject(dir, io.Now)
	if err != nil {
		return fail(io, "cannot use %s: %v", dir, err)
	}
	proj.Sweep()
	state.ExcludeFromGit(proj.Root)
	logf := fileLogger(filepath.Join(dir, "baton.log"), instance, io.Now)
	session, args := sessionOf(args)

	claude := io.Env("BATON_CLAUDE")
	if claude == "" {
		claude = "claude"
	}
	cfg := config.Load(batonRoot(io), io.Env)
	autocompact, valves, err := contextSettings(cfg, args, io.Env)
	if err != nil {
		return fail(io, "%v (set it in %s, or BATON_AUTOCOMPACT)", err, config.Path(batonRoot(io)))
	}
	escalation, err := cfg.Escalation()
	if err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	if _, err := cfg.JobTimeout(); err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	switchAfter, err := cfg.SwitchWait()
	if err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	foreground, err := cfg.ForegroundWait()
	if err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	workers, auto, err := cfg.WorkerCap(io.reading)
	if err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	usageMax, err := cfg.WorkerUsage()
	if err != nil {
		return fail(io, "%v (set it in %s)", err, config.Path(batonRoot(io)))
	}
	timing := loop.DefaultTiming
	timing.EscalationTimeout, timing.SwitchAfter = escalation.Timeout, switchAfter
	// Shorter watchdog timings, for tests and impatient humans.
	for name, field := range map[string]*time.Duration{"BATON_IDLE_NUDGE": &timing.IdleNudge, "BATON_WARN_TIMEOUT": &timing.WarnTimeout, "BATON_RESTART_IDLE": &timing.RestartIdle, "BATON_BACKGROUND_IDLE": &timing.BackgroundIdle} {
		if d, err := time.ParseDuration(io.Env(name)); err == nil && d > 0 {
			*field = d
		}
	}
	valves.WarnTimeout = timing.WarnTimeout // the context question says when baton answers it
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_") {
			env = append(env, kv)
		}
	}
	env = backgroundAfter(env, foreground)
	// baton's own settings travel separately: the host drops every BATON_* from the inherited
	// environment, and these must reach the hooks and the status line.
	batonEnv := append(append(valves.Env(), escalation.Env()...), extraEnv...)
	batonEnv = append(batonEnv, workerEnv(workers, auto, usageMax)...)
	if mode := elevate.PermissionMode(args); mode != "" {
		batonEnv = append(batonEnv, "BATON_WORKER_MODE="+mode) // the mode workers start in, which attach warns about
	}
	if home := io.Env("BATON_HOME"); home != "" {
		// baton's home moved: the CLI inside the session must find the same config and records (a
		// `baton exit` leaves its record there for the shell to find).
		batonEnv = append(batonEnv, "BATON_HOME="+home)
	}
	if !cfg.Restarts() {
		batonEnv = append(batonEnv, "BATON_AUTO_RESTART=0") // so the hooks tell the human the session stays put
	}
	if tty := elevate.ProcTTY(strconv.Itoa(os.Getpid())); tty != "" {
		// The terminal the human sees. claude runs on baton's own pseudo-terminal, so its tty is not
		// the one the shell's prompt hook looks for when `baton exit` hands the session back.
		batonEnv = append(batonEnv, "BATON_TTY="+tty)
	}
	runners := newHostRunners(io, exe, proj, instance)
	controller := &loop.Loop{Notify: notify.New(cfg), Project: filepath.Base(proj.Root), Timing: timing, Logf: logf, Version: version.Version,
		Runners: runners}
	if cfg.Restarts() && upgrade.CanRestart && upgrade.Release(version.Version) {
		w := &upgrade.Watch{Root: batonRoot(io), Running: version.Version, Skip: io.Env("BATON_RESTART_SKIP"), Every: 15 * time.Second, Now: io.Now}
		controller.Newer = w.Newer
	}
	coordinator := &loop.Coordinator{Lead: controller, Lanes: runners, Project: proj, Home: batonRoot(io)}
	var sv *host.Supervisor
	if workers > 0 { // with auto, a machine too small for a worker beside the lead runs none
		// Workers start the way the lead did, with its own flags, beside it in worktrees of their own.
		coordinator.Fleet = &loop.Fleet{Max: workers, UsageMax: usageMax, MaxAuto: escalation.MaxAuto,
			Launch: loop.Launch{Args: elevate.KeepWorkerArgs(args), Autocompact: autocompact, Settings: host.SettingsJSON(exe),
				Env: env, BatonEnv: batonEnv, Bin: exe, Version: version.Version, ScreenDump: io.Env("BATON_SCREEN_DUMP") == "1"},
			Spawn: func(spec host.Spec) (loop.Hidden, error) {
				s, err := sv.Spawn(spec)
				if err != nil {
					return nil, err
				}
				return s, nil
			}}
	}
	sv = host.NewSupervisor(host.Config{
		Claude: claude, Args: args, BatonBin: exe, Project: proj, Instance: instance, Session: session, Version: version.Version,
		Autocompact: autocompact, Stdin: os.Stdin, Stdout: os.Stdout, Env: env, BatonEnv: batonEnv, Now: io.Now, Logf: logf,
		Controller: coordinator, ViewSocket: host.ViewSocket(instance),
	})
	code, err := sv.Run()
	if to, session, ok := controller.Restart(); ok && err == nil {
		// The workers' jobs go on, as the lead's do: the restarted host watches them, and resumes the workers.
		for _, w := range coordinator.Ended("baton restarted on baton " + to) {
			proj.Unbind(w.Instance)
		}
		host.Release(proj, instance)
		controller.Flush()
		return restartOn(io, to, session, args, logf)
	}
	defer host.Release(proj, instance)
	// The workers ended with the lead: the next host for the run resumes them, and nothing would see
	// their jobs' results meanwhile.
	for _, w := range coordinator.Ended("the baton session ended") {
		if ws, err := proj.ExistingRun(w.Ref, w.Instance); err == nil {
			endJobs(io, ws, w.Instance)
		}
		proj.Unbind(w.Instance)
	}
	controller.Ended() // save uncommitted work as the session ends
	if st, ok := proj.Lookup("", instance); ok {
		leftBaton(io, st)
		endJobs(io, st, instance)
	}
	lanesDown(io, proj.Root, proj.Dir, instance)
	if err != nil {
		return fail(io, "%v", err)
	}
	if st, ok := proj.Lookup("", instance); ok && code != 0 && code != 130 && code != 143 { // not a deliberate Ctrl-C or termination
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

// workerEnv passes the workers' settings to the sessions the host runs (n workers at once, or auto, and
// worker_usage_max), so that the hooks, the status line and the commands inside them count the workers
// the host runs: with auto, they read the machine as the host did.
func workerEnv(n int, auto bool, usageMax float64) []string {
	setting := strconv.Itoa(n)
	if auto {
		setting = "auto"
	}
	return []string{"BATON_WORKERS=" + setting, "BATON_WORKER_USAGE_MAX=" + strconv.FormatFloat(usageMax, 'f', -1, 64)}
}

// autoBackground is Claude Code's cap, in milliseconds, on how long a command runs in the foreground
// before it is moved to the background (spike 20).
const autoBackground = "CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS"

// backgroundAfter caps how long claude runs a command in the foreground at d (foreground_max): a command
// still running then moves to the background, where it keeps running and wakes the session when it ends,
// so a long test run or a CI watch never holds the session. A cap the human set themselves stays, and 0
// (off) leaves Claude Code's own.
func backgroundAfter(env []string, d time.Duration) []string {
	if d <= 0 || slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, autoBackground+"=") }) {
		return env
	}
	return append(env, autoBackground+"="+strconv.FormatInt(d.Milliseconds(), 10))
}

// restartOn hands this process over to baton `to`, resuming the session on it: the same pid, terminal
// and claude flags, with the new binary hosting the same conversation. It returns only if that failed,
// after trying to resume the session on this binary instead (which then leaves `to` alone).
func restartOn(io IO, to, session string, args []string, logf func(string, ...any)) int {
	argv := append(append([]string{"baton"}, elevate.KeepUserArgs(args)...), "--resume", session)
	env := os.Environ()
	path := upgrade.Bin(batonRoot(io), to)
	v, err := upgrade.Probe(path)
	if err == nil && v != to {
		err = fmt.Errorf("it reports version %q", v)
	}
	if err == nil {
		fmt.Fprintf(io.Out, "baton: restarting this session on baton %s (was %s)…\n", to, version.Version)
		logf("host: restarting on baton %s: %s %q", to, path, argv)
		err = upgrade.Exec(path, argv, env)
	}
	logf("host: could not restart on baton %s: %v", to, err)
	fmt.Fprintf(io.Err, "baton: could not restart on baton %s (%v); resuming the session on %s\n", to, err, version.Version)
	exe, xerr := os.Executable()
	if xerr == nil {
		xerr = upgrade.Exec(exe, argv, append(withoutVar(env, "BATON_RESTART_SKIP"), "BATON_RESTART_SKIP="+to))
	}
	return fail(io, "could not resume the session either (%v); resume it with: baton --resume %s", xerr, session)
}

// withoutVar copies env without the variable name: on exec, the first of two entries would win.
func withoutVar(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// sessionOf works out the session id claude will run under, when baton can know it at launch: the
// session being resumed by id, or, for a new session, one baton chooses and passes as --session-id, so
// the run is bound before claude has drawn anything. It returns "" when claude picks the session itself
// (--continue, the --resume picker, a fork, a PR or cloud session); the session's first hook binds it.
func sessionOf(args []string) (string, []string) {
	var resumed string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		switch name {
		case "--session-id":
			if !hasVal && i+1 < len(args) {
				val = args[i+1]
			}
			return val, args
		case "-c", "--continue", "--fork-session", "--from-pr", "--teleport", "--cloud", "--bg", "--background", "-p", "--print", "--no-session-persistence":
			return "", args
		case "-r", "--resume":
			if !hasVal && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				val, hasVal = args[i+1], true
			}
			if !hasVal || !looksLikeSessionID(val) {
				return "", args // the picker, or a search term
			}
			resumed = val
		}
	}
	if resumed != "" {
		return resumed, args
	}
	// Before "--", if there is one: what follows it is the prompt.
	id := state.NewSessionID()
	at := slices.Index(args, "--")
	if at < 0 {
		at = len(args)
	}
	return id, slices.Insert(slices.Clone(args), at, "--session-id", id)
}

// looksLikeSessionID reports a UUID, the form Claude Code's session ids take.
func looksLikeSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
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
		if p, err := state.OpenProject(dir, io.Now); err == nil {
			if s, ok := p.Lookup(in.SessionID, io.Env("BATON_INSTANCE")); ok {
				st, _ = recordContext(s, in, io)
				if recordUsage(s, in, io) {
					st, _ = s.Load() // the segment shows it
				}
				if p, err := s.LoadPlan(); err == nil {
					pl, havePlan = p, true
				}
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
	fmt.Fprint(io.Out, statusline.Compose(statusline.Segment(st, pl, havePlan, io.Now(), usageShown(io)), user))
	return 0
}

// usageShown is the share of the five-hour usage at or above which the status line shows it: no new
// worker starts above it (worker_usage_max). 0, for none shown, with no check or no workers.
func usageShown(io IO) float64 {
	cfg := config.Load(batonRoot(io), io.Env)
	f, err := cfg.WorkerUsage()
	if err != nil || f == 0 {
		return 0
	}
	if n, _, err := cfg.WorkerCap(io.reading); err != nil || n == 0 {
		return 0
	}
	return f
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

// usageFresh is how long a reading of the account's usage stands. The status line refreshes every few
// seconds in each session of a run, all of them reading the same account's: the same reading is written
// again only once it is that old.
const usageFresh = 5 * time.Minute

// recordUsage stores the account's five-hour usage Claude Code reports in the run's state (a worker's, in
// the run it works for), where the run's host reads it: no new worker starts above worker_usage_max. A
// reading is written when it changed, or once it is usageFresh old. It reports whether it wrote one.
func recordUsage(s *state.Store, in statusline.Input, io IO) bool {
	now := io.Now()
	used, resets, ok := in.FiveHour(now)
	if !ok {
		return false
	}
	if s.Parent != "" {
		up, err := s.ParentRun()
		if err != nil {
			return false
		}
		s = up
	}
	st, err := s.Load()
	if u := st.Usage; err != nil || u != nil && u.UsedPct == used && u.Resets.Equal(resets) && now.Sub(u.At) < usageFresh {
		return false
	}
	_, err = s.Update(func(st *state.State) error {
		var u state.Usage
		if st.Usage != nil {
			u = *st.Usage
		}
		u.UsedPct, u.Resets, u.At = used, resets, now
		st.Usage = &u
		return nil
	})
	return err == nil
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
func batonRoot(io IO) string { return config.Root(io.Env) }

// homeDir prefers the injected HOME so tests never touch the developer's real settings.
func homeDir(io IO) string {
	if h := io.Env("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}
