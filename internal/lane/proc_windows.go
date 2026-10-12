//go:build windows

package lane

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// shell runs a command line the way a terminal would.
func shell(command string) []string { return []string{"cmd", "/C", command} }

// Join is the command line that runs words as they are (a command given as words, `baton verify -- go
// test ./...`): each word with a space or a character cmd reads otherwise is double-quoted, its quotes
// escaped as a program's arguments are.
func Join(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = w
		if w == "" || strings.ContainsAny(w, " \t\"&|<>^()") {
			quoted[i] = `"` + strings.ReplaceAll(w, `"`, `\"`) + `"`
		}
	}
	return strings.Join(quoted, " ")
}

// Windows has no process groups to signal: a command is stopped on its own.
func setGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, _ bool) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killGroup(*exec.Cmd) {}

// Spawn starts argv detached from this console, its output appended to the file out, and returns its pid.
func Spawn(argv, env []string, dir, out string) (int, error) {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, dir, f, f
	const detached = 0x00000008 // DETACHED_PROCESS
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detached}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, nil
}

// Alive reports whether process pid is running.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	const stillActive = 259
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

// Terminate stops process pid. Windows has no SIGTERM: the process is killed.
func Terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// IsRunner reports whether process pid is a job's runner. Windows has no process table baton reads: no
// pid is taken for one.
func IsRunner(int) bool { return false }

// KillGroup kills the process a job's runner started; Windows has no process groups.
func KillGroup(pgid int) error { return Terminate(pgid) }
