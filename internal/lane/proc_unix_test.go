//go:build !windows

package lane

import "syscall"

// running reports whether a process with this pid exists (a zombie counts until it is reaped).
func running(pid int) bool { return syscall.Kill(pid, 0) == nil }

func pgid(pid int) int {
	g, _ := syscall.Getpgid(pid)
	return g
}
