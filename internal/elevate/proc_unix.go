//go:build !windows

package elevate

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Supported reports whether elevation works on this platform.
const Supported = true

// ProcTTY returns the controlling terminal of a process ("ttys003", "pts/3"), or "" if it has none.
func ProcTTY(pid string) string {
	t := psField(pid, "tty=")
	if t == "" || t == "?" || t == "??" {
		return ""
	}
	return t
}

// ProcArgs returns a process's command line, split on whitespace (quoting is lost, which is fine for the
// flags KeepArgs keeps).
func ProcArgs(pid string) []string {
	return strings.Fields(psField(pid, "args="))
}

func psField(pid, field string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", field, "-p", pid).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Detach starts cmd in its own session so it outlives the hook that started it.
func Detach(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	return cmd.Start()
}

// Stop sends SIGTERM to pid and waits for it to exit, escalating to SIGKILL after grace.
func Stop(pid int, grace time.Duration) (killed bool) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if p.Signal(syscall.Signal(0)) != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.Signal(syscall.SIGKILL)
	return true
}
