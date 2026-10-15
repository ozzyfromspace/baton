package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestWaitWords(t *testing.T) {
	gate := state.Job{ID: "j3", Phase: "P4", Kind: state.JobVerify, Command: "make e2e", Commit: "0123456789abcdef", Here: true}
	for _, c := range []struct {
		name, got, want string
	}{
		{"the session waits on its gate", GateWait("P4 (Gates)", gate),
			"baton: P4 (Gates) waits on job j3 (`make e2e`) — nothing is needed from you: Claude is given its result when it ends"},
		{"no phase until a job ends", NoPhaseYet("job j1 (`make test`, P0)", nil),
			"baton: no phase can start until job j1 (`make test`, P0) finishes — nothing is needed from you: baton takes the next one up then"},
		{"no phase while a gate runs here", NoPhaseYet("job j3 (`make e2e`, P4)", &gate),
			"baton: job j3 (`make e2e`, P4) runs its gate in this working tree, so no phase starts here until it ends — nothing is needed from you: baton takes the next one up then"},
		{"no phase and no job", NoPhaseYet("", nil), "baton: no phase can start yet; baton takes the next one up as soon as one can"},
		{"a run that stopped moving", RunStalled(41*time.Minute, []string{"P3: agent between turns 41m", "P4: verifying 50m (job P4.j1), " + LogQuiet(35*time.Minute)}, "lead: idle, with no phase in hand"),
			"the run has not moved for 41m — P3: agent between turns 41m; P4: verifying 50m (job P4.j1), its log has not grown for 35m; lead: idle, with no phase in hand. " +
				"/baton board shows where it stands"},
		{"a gate waits for the one lane", LaneWaits(gate, 1, 0),
			"baton: job j3 (`make e2e`, P4) waits for a lane: the project's one lane is in use. It starts once one is free"},
		{"a gate waits behind another", LaneWaits(gate, 3, 1),
			"baton: job j3 (`make e2e`, P4) waits for a lane: all 3 of the project's lanes are in use, and 1 gate waits ahead of it. It starts once one is free"},
		{"a gate waits behind others", LaneWaits(gate, 2, 4),
			"baton: job j3 (`make e2e`, P4) waits for a lane: all 2 of the project's lanes are in use, and 4 gates wait ahead of it. It starts once one is free"},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}
