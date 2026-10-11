//go:build !windows

package lane

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// shell runs a command line the way a terminal would.
func shell(command string) []string { return []string{"sh", "-c", command} }

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
