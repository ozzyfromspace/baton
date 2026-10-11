package procs

import "time"

// Stop stops nothing on Windows, where baton reads no process table (List): it returns pids as they are.
func Stop(pids []int, _ time.Duration) []int { return pids }

// Group is 0 on Windows, which has no process groups.
func Group() int { return 0 }
