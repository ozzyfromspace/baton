// Package hostcheck tells a session baton hosts from one that only inherited baton's environment.
//
// The host sets BATON_HOST=1 (with BATON_INSTANCE, BATON_DIR and the rest) for the claude it starts, and
// everything that claude starts inherits them. So does a conversation Claude Code moves to its background
// service (claude daemon → bg-pty-host → bg-spare, on 2.1.300), under a new session id: it looks hosted,
// yet no baton host is above it, so nothing compacts it, starts its phase agents or runs its plan. Hosted
// therefore means a live baton host above the process asking: the host records its process where its
// instance is linked (state.Project.SetHostPID), and Check walks the asking process's ancestors to it.
//
// The walk reads one process at a time (procs.Parent), a few microseconds each, never the whole table, so
// every hook and command asks afresh; a command that asks more than once keeps its first answer (cli's
// IO.Hosted).
package hostcheck

import (
	"os"

	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Status is how baton stands to the session a process runs in.
type Status int

const (
	// Plain is a session baton has nothing to do with: BATON_HOST is not set.
	Plain Status = iota
	// Hosted is a session a live baton host is above.
	Hosted
	// Inherited is a session that has BATON_HOST=1 from the environment it was started in, with no baton
	// host above it. Its hooks and baton's commands leave it alone (hooks.DispatchAs).
	Inherited
)

// Reader reads one process: its parent's pid and the name of its executable (procs.Parent).
type Reader func(pid int) (ppid int, name string, err error)

// hostName is the name a baton host's process runs under: what says it is one when the host recorded no
// process, as one of v0.8.3 or before did not.
const hostName = "baton"

// maxDepth bounds the walk up a process's ancestors: a real chain is a dozen deep at most.
const maxDepth = 64

// Check says how baton stands to the session process self runs in, by its environment (env) and its
// ancestors (read). With BATON_HOST=1, it is hosted when the host process recorded for BATON_INSTANCE in
// BATON_DIR is an ancestor of self, or, with none recorded, when a process named baton is; otherwise it
// only inherited the variable. Where processes cannot be read (read is nil, or fails), the variable is
// trusted, as before v0.8.4: a hosted session taken for an inherited one would stop baton altogether.
func Check(env func(string) string, self int, read Reader) Status {
	if env("BATON_HOST") != "1" {
		return Plain
	}
	if read == nil {
		return Hosted
	}
	host := state.HostPID(env("BATON_DIR"), env("BATON_INSTANCE"))
	pid, _, err := read(self)
	for range maxDepth {
		switch {
		case err != nil:
			return Hosted
		case pid <= 1:
			return Inherited // the top of the tree (init, launchd), with no host on the way
		case pid == host:
			return Hosted
		}
		var name string
		if pid, name, err = read(pid); err == nil && host == 0 && name == hostName {
			return Hosted
		}
	}
	return Hosted
}

// Of is Check for this process, reading the machine's processes.
func Of(env func(string) string) Status { return Check(env, os.Getpid(), procs.Parent) }

// Trust is the status the variable alone gives, as before v0.8.4: for tests, which run under no host.
func Trust(env func(string) string) Status { return Check(env, 0, nil) }
