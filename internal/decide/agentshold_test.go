package decide

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead's stop with no phase in hand, while phase agents' work alone holds the run, says what it waits
// on and that nothing waits on the human; with nothing held, it says nothing, and NoPhaseYet stands.
func TestTheWaitLineNamesTheAgentsWork(t *testing.T) {
	for _, c := range []struct {
		name string
		h    state.AgentsHold
		want string
	}{
		{"the last phase with an agent", state.AgentsHold{AtWork: []string{"P4"}, Last: true},
			"baton: P4, the last phase, is with its phase agent — baton says here when it lands and the plan is complete. Nothing waits on you."},
		{"two last phases", state.AgentsHold{AtWork: []string{"P3", "P4"}, Last: true},
			"baton: P3 and P4, the last phases, are with phase agents — baton says here when they land and the plan is complete. Nothing waits on you."},
		{"a phase waits on an agent's", state.AgentsHold{AtWork: []string{"P3"}, Behind: []state.Behind{{Phase: "P5", On: []string{"P3"}}}},
			"baton: no phase can start until P3's agent lands its work (P5 waits on it) — baton takes the next one up then. Nothing waits on you."},
		{"two phases wait on an agent's", state.AgentsHold{AtWork: []string{"P3", "P4"}, Behind: []state.Behind{{Phase: "P5", On: []string{"P3"}}, {Phase: "P6", On: []string{"P3"}}}},
			"baton: no phase can start until P3's agent lands its work (P5 and P6 wait on it) — baton takes the next one up then. Nothing waits on you."},
		{"a phase waits on two agents'", state.AgentsHold{AtWork: []string{"P3", "P4"}, Behind: []state.Behind{{Phase: "P5", On: []string{"P3", "P4"}}}},
			"baton: no phase can start until P3's and P4's agents land their work (P5 waits on them) — baton takes the next one up then. Nothing waits on you."},
		{"phases wait on different agents'", state.AgentsHold{AtWork: []string{"P2", "P3", "P4"},
			Behind: []state.Behind{{Phase: "P5", On: []string{"P3"}}, {Phase: "P6", On: []string{"P2", "P4"}}}},
			"baton: no phase can start until phase agents land their work (P5 waits on P3's agent, P6 on P2's and P4's agents) — baton takes the next one up then. Nothing waits on you."},
		{"agents at work, nothing held", state.AgentsHold{AtWork: []string{"P3"}}, ""},
		{"nothing at all", state.AgentsHold{}, ""},
	} {
		if got := AgentsHold(c.h); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
