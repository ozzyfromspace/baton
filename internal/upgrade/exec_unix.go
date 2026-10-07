//go:build !windows

package upgrade

import "syscall"

// CanRestart reports whether this platform can hand a running baton over to another binary in place.
const CanRestart = true

const exeSuffix = false

// Exec replaces this process with path, keeping its pid, its terminal and its place in the shell's job
// control. It only returns if the exec failed.
func Exec(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }
