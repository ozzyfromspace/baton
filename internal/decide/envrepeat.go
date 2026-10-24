package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Two whole runs of a phase's gate that lose a stage to the same kind push the human, once per phase and
// kind (v0.8.17, loop/envrepeat.go): information, as the model works around it meanwhile.

// EnvRepeated is the push, once per phase and kind, that a second whole run of r.Phase's gate lost a stage
// to the same kind as an earlier one: "P15's gate lost `check-bundle-budgets` to memory in two whole runs
// (P15.j1, P15.j4): the machine, Docker's VM say, may be too small for it. The agent works around it
// meanwhile." It is information only: agent says whether a phase agent works on the phase, or the lead.
func EnvRepeated(r state.EnvRepeat, agent bool) string {
	names := make([]string, len(r.Stages))
	for i, n := range r.Stages {
		names[i] = "`" + n + "`"
	}
	it := plural(len(r.Stages), "it", "them")
	to, why := r.Kind, "its environment fails "+it+" the same way each time"
	if r.Kind == lane.EnvKilled {
		to, why = "memory", "the machine, Docker's VM say, may be too small for "+it
	}
	who := "The lead"
	if agent {
		who = "The agent"
	}
	return fmt.Sprintf("%s's gate lost %s to %s in two whole runs (%s): %s. %s works around it meanwhile.",
		r.Phase, andList(names), to, strings.Join(r.Jobs, ", "), why, who)
}
