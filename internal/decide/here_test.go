package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestHereWords(t *testing.T) {
	gate := state.Job{ID: "j3", Phase: "P4", Kind: state.JobVerify, Command: "make e2e", Commit: "0123456789abcdef", Here: true}
	lane := state.Job{ID: "j1", Phase: "P0", Command: "make test", Commit: "0123456789abcdef"}
	for _, c := range []struct {
		name, got, want string
	}{
		{"a gate here, as it ran", Ran(gate), "`make e2e` on 0123456789ab in the working tree"},
		{"a gate in a lane", Ran(lane), "`make test` on 0123456789ab"},
		{"done while it runs", HereHeld(gate), "Job j3 runs in this working tree, so no other phase starts here until it ends. End your turn now; baton wakes you when there is something to do."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}

	// A lane error here is not a lane's: it says nothing of lane scripts.
	failed := gate
	failed.Status = state.JobLaneError
	if r := JobResult(failed, "", "", nil); !strings.Contains(r, "the gate's command never ran") || strings.Contains(r, ".baton-lane") {
		t.Errorf("a lane error here: %s", r)
	}
	// With a lane script, the refusal of a gate naming the checkout does not offer one.
	if r := CheckoutNamed("cd /w/p && make e2e", "/w/p", "", true); strings.Contains(r, ".baton-lane") || !strings.Contains(r, "baton verify --here") {
		t.Errorf("with a lane script: %s", r)
	}
	if r := CheckoutNamed("cd /w/p && make e2e", "/w/p", "", false); !strings.Contains(r, "commit an executable .baton-lane script") {
		t.Errorf("without a lane script: %s", r)
	}
	for _, worker := range []bool{false, true} {
		r := HereQueued(worker, "3h00m")
		if strings.Contains(r, "lands no worker's work") == worker || !strings.Contains(r, "If it runs past 3h00m") {
			t.Errorf("worker %v: %s", worker, r)
		}
	}
}
