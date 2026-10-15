package cli

import "github.com/ozzyfromspace/baton/internal/procs"

// Usage reads the CPU time a job's process tree has used, for telling a gate that hangs (loop's hung).
func (h hostRunners) Usage(pgid int) (procs.Usage, error) { return procs.TreeUsage(pgid) }
