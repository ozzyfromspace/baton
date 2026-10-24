package decide

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The push names the phase, the stages, the kind and the two runs, says what may be wrong, and who works
// around it meanwhile.
func TestEnvRepeatedPush(t *testing.T) {
	for _, c := range []struct {
		r     state.EnvRepeat
		agent bool
		want  string
	}{
		{state.EnvRepeat{Phase: "P15", Kind: lane.EnvKilled, Stages: []string{"check-bundle-budgets"}, Jobs: []string{"P15.j1", "P15.j4"}}, true,
			"P15's gate lost `check-bundle-budgets` to memory in two whole runs (P15.j1, P15.j4): the machine, Docker's VM say, may be too small for it. " +
				"The agent works around it meanwhile."},
		{state.EnvRepeat{Phase: "P5", Kind: lane.EnvKilled, Stages: []string{"build", "e2e"}, Jobs: []string{"j2", "j6"}}, false,
			"P5's gate lost `build` and `e2e` to memory in two whole runs (j2, j6): the machine, Docker's VM say, may be too small for them. " +
				"The lead works around it meanwhile."},
		{state.EnvRepeat{Phase: "P5", Kind: "a port already in use", Stages: []string{"up"}, Jobs: []string{"j2", "j6"}}, true,
			"P5's gate lost `up` to a port already in use in two whole runs (j2, j6): its environment fails it the same way each time. " +
				"The agent works around it meanwhile."},
	} {
		if got := EnvRepeated(c.r, c.agent); got != c.want {
			t.Errorf("got\n%s\nwant\n%s", got, c.want)
		}
	}
}
