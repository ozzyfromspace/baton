package loop

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// While the lead's phase boundary owes a replan of baton's own (here, after a phase marked Replan: after),
// no new phase agent starts, since one would take a phase the replan restructures first: for at most
// ReplanMax, and not at all with auto_replan off.
func TestNoNewAgentWhileTheBoundaryOwesAReplan(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pl := plan.Plan{Phases: []plan.Phase{{ID: "P0", After: []string{}, Replan: plan.ReplanAfter},
		{ID: "P1", After: []string{"P0"}}, {ID: "P2", After: []string{"P0"}}, {ID: "P3", After: []string{"P0"}}}}
	st := state.Attach(pl, now, state.Origin{})
	st.RunBranch = "main"
	if _, err := state.Done(&st, pl, "P0", now, state.DoneOpts{}); err != nil || !st.BoundaryOwed || st.Current != "P1" {
		t.Fatalf("P0 not done: %v %+v", err, st)
	}
	for _, c := range []struct {
		name  string
		fleet Fleet
		at    time.Duration
		cued  bool
		want  string // the phase that starts, "" for none
	}{
		{"the boundary owes it", Fleet{Max: 3, AutoReplan: true}, time.Minute, false, ""},
		{"owed longer than ReplanMax", Fleet{Max: 3, AutoReplan: true, ReplanMax: 5 * time.Minute}, 5 * time.Minute, false, "P2"},
		{"owed longer than replan.Max, by default", Fleet{Max: 3, AutoReplan: true}, replan.Max, false, "P2"},
		{"auto_replan off", Fleet{Max: 3}, time.Minute, false, "P2"},
		{"looked at already", Fleet{Max: 3, AutoReplan: true}, time.Minute, true, "P2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := st
			if c.cued {
				s.ReplanCued = []string{state.ReplanMarked + ":P0"}
			}
			phase, why, held := c.fleet.next(s, pl, now.Add(c.at), room{})
			if phase != c.want || held != "" || (c.want == "") != (why == decide.ReplanDueHoldsAgents) {
				t.Errorf("next = %q, %q, %q; want %q", phase, why, held, c.want)
			}
		})
	}
}
