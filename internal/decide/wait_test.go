package decide

import (
	"testing"

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
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}
