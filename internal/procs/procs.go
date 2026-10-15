// Package procs reads this user's processes as they are now, with no cgo, ps or lsof, so that baton clean
// can find what a session that is gone left running (docs/research/workers.md, "Machine readings and
// leftovers", spike 28):
//
//   - macOS: the process table from sysctl kern.proc.uid, each environment from kern.procargs2, and each
//     working directory from proc_info (PROC_PIDVNODEPATHINFO), as lsof reads it. macOS hides the
//     environment of its own platform binaries (/bin/sleep, /bin/zsh, /bin/sh): it comes back empty, with no
//     error, and such a process counts as hidden.
//   - Linux: /proc/<pid>/stat, status, environ and cwd.
//   - Elsewhere (Windows): nothing is read (ErrUnsupported).
//
// A working directory that was removed still reads as its old path (Linux's " (deleted)" is cut off), so
// compare paths as strings: one that is gone cannot be resolved.
//
// It also reads how much CPU time a job's process tree has used (TreeUsage), so that the host can tell a
// gate that hangs from one that works without a word: on macOS from proc_info (PROC_PIDTASKINFO), which
// reads platform binaries too; on Linux from /proc/<pid>/stat.
package procs

import (
	"bytes"
	"errors"
	"strings"
)

// Proc is one process.
type Proc struct {
	PID, PPID, PGID int
	Comm            string // the name of its executable, as the kernel keeps it (cut to 15 or 16 bytes)
	Cwd             string // its working directory, "" when it could not be read
	// Env holds its BATON_* variables. It is nil when its environment could not be read or is hidden
	// (Hidden); an environment that can be read holds at least one variable on macOS.
	Env map[string]string
}

// Hidden reports a process whose environment could not be read: whose it is cannot be told.
func (p Proc) Hidden() bool { return p.Env == nil }

// ErrUnsupported means this system has no process table baton reads.
var ErrUnsupported = errors.New("baton cannot read the process table on this system")

// List reads this user's processes, other than this one and zombies.
func List() ([]Proc, error) { return list() }

// batonEnv keeps the BATON_* variables of an environment ("KEY=value" strings).
func batonEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "BATON_") {
			m[k] = v
		}
	}
	return m
}

// cstr is the string up to the first NUL.
func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Parent reads one process, pid: its parent's pid and the name of its executable (as Proc.Comm). It reads
// that process alone, not the table, so it is cheap enough for every hook (hostcheck walks a session's
// ancestors with it). An error for a process that is gone, and ErrUnsupported where baton reads no
// process table.
func Parent(pid int) (ppid int, comm string, err error) { return parent(pid) }
