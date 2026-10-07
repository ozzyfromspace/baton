package cli

import (
	"encoding/json"
	"fmt"
	goio "io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/state"
)

func init() {
	register("elevate", "hand this plain claude session to baton (same conversation): elevate [what to do next]", cmdElevate)
	register("exit", "leave baton: once the turn ends, this conversation goes on as plain claude in this terminal", cmdExit)
	register("init", "print shell setup for your shell config: eval \"$(baton init zsh|bash)\"", cmdInit)
	// Internal commands, called by the shell hook, the plugin's Stop hook and each other.
	register("relaunch", "(internal) relaunch an elevated session in this terminal", cmdRelaunch)
	register("elevate-stop", "(internal) plugin Stop hook: stop an elevating claude once its turn ends", cmdElevateStop)
	register("elevate-kill", "(internal) stop a claude process cleanly", cmdElevateKill)
}

func cmdElevate(args []string, io IO) int {
	if hosted(io) {
		fmt.Fprintln(io.Out, "baton: this session is already hosted by baton; nothing to do.")
		return 0
	}
	if !elevate.Supported {
		return fail(io, "elevation is not supported on this platform yet; exit and start the session with `baton --resume <session id>`")
	}
	pid, sid := io.Env("CLAUDE_PID"), io.Env("CLAUDE_CODE_SESSION_ID")
	if pid == "" || sid == "" {
		return fail(io, "run this from inside a claude session (CLAUDE_PID and CLAUDE_CODE_SESSION_ID are not set)")
	}
	tty := elevate.ProcTTY(pid)
	if tty == "" {
		return fail(io, "this claude session has no terminal (the desktop app or an IDE panel?), so it cannot be relaunched under baton")
	}
	if !elevate.ShellHookInstalled(homeDir(io), io.Env) {
		fmt.Fprintf(io.Out, "baton: this shell does not relaunch elevated sessions yet, so baton will not stop claude. Either:\n"+
			"  • add  eval \"$(baton init zsh)\"  (or bash) to your shell config and open a new terminal, or\n"+
			"  • exit this session (/exit) and run:  baton --resume %s\n", sid)
		return 0
	}
	cwd, _ := os.Getwd()
	pidN, _ := strconv.Atoi(pid)
	rec := elevate.Record{
		SessionID: sid, Dir: cwd, ClaudePID: pidN, TTY: tty, Args: elevate.KeepArgs(elevate.ProcArgs(pid)),
		Pending: strings.TrimSpace(strings.Join(args, " ")), Created: io.Now(),
	}
	if err := elevate.Save(batonRoot(io), rec); err != nil {
		return fail(io, "cannot record the elevation: %v", err)
	}
	if s, err := store(io); err == nil {
		s.Event("elevate_requested", map[string]any{"tty": tty, "pending": rec.Pending})
	}
	fmt.Fprintln(io.Out, "baton: elevating. When this turn ends, claude restarts under baton in this terminal, resuming this same conversation. End your turn now.")
	return 0
}

// cmdExit is elevation in reverse: the host stops claude once the turn ends, and the shell's prompt hook
// resumes the same conversation as plain claude. A running plan is paused first: nothing would drive it.
func cmdExit(args []string, io IO) int {
	if len(args) != 0 {
		return fail(io, "usage: baton exit")
	}
	if !hosted(io) {
		fmt.Fprintln(io.Out, "baton: this session is not hosted by baton; there is nothing to leave.")
		return 0
	}
	pid, sid := io.Env("CLAUDE_PID"), io.Env("CLAUDE_CODE_SESSION_ID")
	manual := fmt.Sprintf("end this session (/exit) and run:  claude --resume %s", sid)
	if !elevate.Supported || pid == "" || sid == "" {
		fmt.Fprintln(io.Out, "baton: baton cannot hand this session back by itself here. To leave baton, "+manual)
		return 0
	}
	tty := elevate.ProcTTY(pid)
	if tty == "" || !elevate.ShellHookInstalled(homeDir(io), io.Env) {
		fmt.Fprintln(io.Out, "baton: this shell does not resume sessions for baton yet (the `baton init` line is not in its config). To leave baton, "+manual)
		return 0
	}
	s, err := store(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	cwd, _ := os.Getwd()
	pidN, _ := strconv.Atoi(pid)
	rec := elevate.Record{SessionID: sid, Dir: cwd, ClaudePID: pidN, TTY: tty, Args: elevate.KeepArgs(elevate.ProcArgs(pid)), Plain: true, Created: io.Now()}
	if err := elevate.Save(batonRoot(io), rec); err != nil {
		return fail(io, "cannot record the session to resume: %v", err)
	}
	paused := false
	s.Update(func(st *state.State) error {
		paused = state.Pause(st) == nil
		st.Run.ExitAt = io.Now()
		return nil
	})
	s.Event("exit_requested", map[string]any{"paused": paused})
	msg := "baton: leaving baton. When this turn ends, baton stops claude, and this terminal resumes this same conversation as plain Claude Code. End your turn now."
	if paused {
		msg += fmt.Sprintf(" The plan is paused; to pick it up later, start the session with `baton --resume %s`, then /baton resume.", sid)
	}
	fmt.Fprintln(io.Out, msg)
	return 0
}

// leftBaton completes `baton exit` once claude has stopped: it marks the session's record stopped, so the
// shell's prompt hook resumes it as plain claude, and clears the request so the run can be driven again.
func leftBaton(io IO, s *state.Store) {
	var sid string
	s.Update(func(st *state.State) error {
		if !st.Run.ExitAt.IsZero() {
			sid, st.Run.ExitAt = st.Run.SessionID, time.Time{}
		}
		return nil
	})
	if sid == "" {
		return
	}
	root := batonRoot(io)
	if rec, ok := elevate.FindSession(root, sid); ok && rec.Plain && rec.Stopped.IsZero() {
		rec.Stopped = io.Now()
		elevate.Save(root, rec)
	}
}

func cmdInit(args []string, io IO) int {
	if len(args) != 1 {
		return fail(io, "usage: eval \"$(baton init zsh)\"  (or bash)")
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(io, "%v", err)
	}
	script, err := elevate.Init(args[0], exe)
	if err != nil {
		return fail(io, "%v", err)
	}
	fmt.Fprint(io.Out, script)
	return 0
}

// cmdRelaunch runs from the shell's prompt hook. Without a usable record it does nothing, silently,
// because it runs on prompts.
func cmdRelaunch(args []string, io IO) int {
	p, err := parseArgs(args, []string{"tty"}, nil)
	if err != nil || p.vals["tty"] == "" {
		return 0
	}
	rec, err := elevate.Take(batonRoot(io), p.vals["tty"], io.Now())
	if err == elevate.ErrNothingToRelaunch {
		return 0
	}
	if err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n", err)
		return 0
	}
	if err := os.Chdir(rec.Dir); err != nil {
		fmt.Fprintf(io.Err, "baton: cannot return to %s: %v\n", rec.Dir, err)
		return 0
	}
	claudeArgs := append([]string{"--resume", rec.SessionID}, rec.Args...)
	if rec.Plain {
		fmt.Fprintf(io.Out, "baton: resuming session %s as plain Claude Code…\n", short(rec.SessionID))
		return runPlain(claudeArgs, io)
	}
	fmt.Fprintf(io.Out, "baton: relaunching session %s under baton…\n", short(rec.SessionID))
	return runHostWith(claudeArgs, io, []string{"BATON_PENDING=" + rec.Pending})
}

// runPlain runs claude in this terminal with none of baton's environment.
func runPlain(args []string, io IO) int {
	claude := io.Env("BATON_CLAUDE")
	if claude == "" {
		claude = "claude"
	}
	cmd := exec.Command(claude, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(io.Err, "baton: %v\n", err)
		return 1
	}
	return 0
}

// cmdElevateStop is the plugin's Stop hook in plain sessions. Like every hook it fails open (exit 0).
func cmdElevateStop(_ []string, io IO) (code int) {
	defer func() {
		if r := recover(); r != nil {
			code = 0
		}
	}()
	if hosted(io) {
		return 0
	}
	raw, _ := goio.ReadAll(goio.LimitReader(io.In, 1<<20))
	var in struct {
		SessionID       string `json:"session_id"`
		BackgroundTasks []struct {
			Type string `json:"type"`
		} `json:"background_tasks"`
	}
	if json.Unmarshal(raw, &in) != nil || in.SessionID == "" {
		return 0
	}
	root := batonRoot(io)
	rec, ok := elevate.FindSession(root, in.SessionID)
	if !ok || !rec.Stopped.IsZero() || rec.Plain {
		return 0
	}
	for _, t := range in.BackgroundTasks {
		if t.Type != "monitor" {
			say(io, "baton: elevation waits until background work finishes (it would be stopped with claude)")
			return 0
		}
	}
	rec.Stopped = io.Now()
	if elevate.Save(root, rec) != nil {
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	if elevate.Detach(exec.Command(exe, "elevate-kill", strconv.Itoa(rec.ClaudePID))) == nil {
		say(io, "baton: restarting this session under baton…")
	}
	return 0
}

func cmdElevateKill(args []string, io IO) int {
	if len(args) != 1 {
		return 1
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil || pid <= 1 {
		return 1
	}
	time.Sleep(1500 * time.Millisecond) // let the Stop hook return and the turn finish rendering
	elevate.Stop(pid, 5*time.Second)
	return 0
}

func say(io IO, msg string) {
	b, _ := json.Marshal(map[string]string{"systemMessage": msg})
	fmt.Fprintln(io.Out, string(b))
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
