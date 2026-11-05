package cli

import (
	"os"

	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/procs"
)

// taskProcs is how the host finds the model's background shells among claude's children, reads their process
// trees' CPU, and ends one baton's reviewer found does nothing more for the run where TaskStop did not
// (loop.TaskProcs): claude is the host's own child.
func taskProcs() loop.TaskProcs {
	return loop.TaskProcs{Host: os.Getpid(), List: procs.List, Usage: procs.TreeUsage, Stop: procs.Stop}
}
