package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A gate that failed for its environment, not its change (lane.Environment): its result says so, and lists
// the run's environment facts so far (facts.go).

// Env is what a red gate's log says of its environment (lane.Environment: what failed it, and the line
// that says so), with the run's environment facts so far.
type Env struct {
	Kind, Line string
	Facts      []state.Fact
}

// EnvFailed is what JobResult says of red gate j, whose log shows env and no test failure: it looks like
// the environment, not the change, so the model fixes that and verifies again, records what the
// environment needs, and makes it permanent in .baton-lane (a gate run in place has no lane).
func EnvFailed(j state.Job, env Env) string {
	var b strings.Builder
	if j.Status == state.JobTimeout {
		b.WriteString("It ran past verify_timeout, so baton stopped it: that counts as a failure. ")
	}
	fmt.Fprintf(&b, "This looks like the gate's environment, not the change: no test failed, and its log shows %s:\n\n    %s\n\n", env.Kind, env.Line)
	fmt.Fprintf(&b, "So do not look for a bug in %s's code because of it. Fix the environment (or the gate's setup of it), then verify again with the same command, `%s`: "+
		"until it passes, %s's gate stands red and `baton done %s` is refused.\n", j.Phase, j.Command, j.Phase, j.Phase)
	b.WriteString("Once you know what the environment needs, record it with `baton env \"<what the environment needs>\"` (a port to leave free, a service to start first), " +
		"so that every later brief in this run has it")
	if j.Here {
		b.WriteString(".\n")
	} else {
		b.WriteString(", and make it permanent in .baton-lane, which sets up every lane.\n")
	}
	if len(env.Facts) == 0 {
		b.WriteString("No environment facts are on record for this run yet.")
		return b.String()
	}
	fmt.Fprintf(&b, "The run's environment facts so far (one may be what this gate missed):\n%s", factList(env.Facts))
	return b.String()
}
