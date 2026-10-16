package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A replan open for Timing.ReplanMax lapses, running or paused: the plan goes on as it was, and the human
// is told. A sleep starts its clock over.
func TestAnOpenReplanLapses(t *testing.T) {
	for _, c := range []struct {
		name  string
		pause bool
		sleep bool
	}{
		{"running", false, false},
		{"paused", true, false},
		{"across a sleep", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.set(func(st *state.State) {
				st.Replan = &state.Replan{By: state.ReplanHuman, Since: r.now, N: 1}
				if c.pause {
					st.Mode = state.ModePaused
				}
			})
			r.tick(0)
			if c.sleep {
				r.tick(10 * time.Minute) // the machine slept: the replan's clock starts over at the wake
			}
			for range 19 {
				r.tick(time.Minute)
			}
			if r.state().Replan == nil {
				t.Fatal("lapsed early")
			}
			r.tick(time.Minute)
			if st := r.state(); st.Replan != nil || !strings.Contains(r.events(), `"kind":"replan_lapsed"`) {
				t.Fatalf("not lapsed: %+v\n%s", st.Replan, r.events())
			}
			if a := r.state().Run.Announce; len(a) == 0 || !strings.Contains(a[len(a)-1], "the replan was not finished within 20 minutes") {
				t.Errorf("announced %q", a)
			}
		})
	}
}

// No new phase agent starts while a replan is open; the lead's phase and agents at work go on.
func TestNoNewAgentWhileAReplanIsOpen(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pl := plan.Plan{Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{}}, {ID: "P2", After: []string{}}}}
	st := state.Attach(pl, now, state.Origin{})
	st.RunBranch = "main"
	f := Fleet{Max: 3}
	if phase, why, _ := f.next(st, pl, now, room{}); phase != "P1" || why != "" {
		t.Fatalf("with no replan open: %q, %q", phase, why)
	}
	st.Replan = &state.Replan{By: state.ReplanHuman, Since: now, N: 1}
	if phase, why, held := f.next(st, pl, now, room{}); phase != "" || why != decide.ReplanHoldsAgents || held != "" {
		t.Errorf("with a replan open: %q, %q, %q", phase, why, held)
	}
}
