package decide

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton's question about a whole run after one that lost a stage to its environment names each stage, its
// line and the ways out; and a run that answers it with --why says so to the human.
func TestEnvAgainAsks(t *testing.T) {
	last := state.Job{ID: "j4", Command: "bin/qa-set"}
	build := state.EnvLoss{Stage: "check-bundle-budgets", Kind: lane.EnvKilled, Line: "JavaScript heap out of memory"}
	up := state.EnvLoss{Stage: "up", Kind: "a port already in use", Line: "port is already allocated"}
	for _, c := range []struct {
		name string
		lost []state.EnvLoss
		want string
	}{
		{"killed", []state.EnvLoss{build}, "Its last whole run, job j4, lost `check-bundle-budgets` to the machine (`JavaScript heap out of memory`). " +
			"Rerun it alone with `baton verify --failed`. A whole run puts it beside the heaviest stages again, so before one, give it room: " +
			"order the gate so it runs apart, or free memory, and record what it needs with `baton env`."},
		{"both", []state.EnvLoss{build, up}, "Its last whole run, job j4, lost `check-bundle-budgets` to the machine (`JavaScript heap out of memory`) and " +
			"`up` to a port already in use (`port is already allocated`). Rerun it alone with `baton verify --failed`. A whole run puts it beside the heaviest stages again, " +
			"so before one, give it room: order the gate so it runs apart, or free memory, and record what it needs with `baton env`. " +
			"Fix the environment before a whole run, rerun it alone with `baton verify --failed`, and record what it needs with `baton env`."},
	} {
		if got := EnvAgain(last, c.lost); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	if got := GateAnswered("P15", "bin/qa-set", []string{GateEnvAgain}, "the build runs apart now"); got !=
		"P15 runs `bin/qa-set` over baton's question (its last whole run lost a stage to its environment): the build runs apart now" {
		t.Errorf("answered: %s", got)
	}
}
