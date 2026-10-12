//go:build !windows

package lane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// shell runs a command line the way a terminal would.
func shell(command string) []string { return []string{"sh", "-c", command} }

// Join is the command line that runs words as they are (a command given as words, `baton verify -- sh
// check.sh`): each word quoted for shell where it would read it otherwise.
func Join(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = w
		if w == "" || strings.IndexFunc(w, func(r rune) bool { return !plain(r) }) >= 0 {
			quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// plain is a character sh reads as itself anywhere in a word.
func plain(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r)
}

// setGroup starts cmd in a process group of its own, so everything it starts can be stopped together.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalGroup asks cmd's process group to stop (SIGTERM), or kills it outright.
func signalGroup(cmd *exec.Cmd, kill bool) error {
	if cmd.Process == nil {
		return nil
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// killGroup kills whatever cmd left running in its process group, such as a server a gate started in the
// background and never stopped.
func killGroup(cmd *exec.Cmd) { signalGroup(cmd, true) }

// Spawn starts argv detached: in a session of its own, with no terminal, its output appended to the file
// out, and orphaned at once, so it outlives whoever started it (even a baton that restarts itself in
// place) and the system reaps it when it ends. It returns the pid.
func Spawn(argv, env []string, dir, out string) (int, error) {
	const detach = `out=$1; shift; "$@" </dev/null >>"$out" 2>&1 & echo $!`
	cmd := exec.Command("/bin/sh", append([]string{"-c", detach, "sh", out}, argv...)...)
	cmd.Env, cmd.Dir = env, dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	b, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("starting %s: no pid (%q)", argv[0], b)
	}
	return pid, nil
}

// Alive reports whether process pid is running.
func Alive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Terminate asks process pid to stop (SIGTERM). A job's runner stops its command, and brings its lane
// down, when it gets one.
func Terminate(pid int) error {
	if pid <= 1 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// IsRunner reports whether process pid is a job's runner (`baton lane-run`), as the process table has it
// now: a pid on record may since belong to another program.
func IsRunner(pid int) bool {
	if pid <= 1 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && runnerArgs(strings.Fields(string(out)))
}

// KillGroup kills process group pgid outright: whatever a job's runner left behind when it died.
func KillGroup(pgid int) error {
	if pgid <= 1 {
		return nil
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
