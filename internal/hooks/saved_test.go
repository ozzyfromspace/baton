package hooks

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead's stop that reports the plan complete says what phase agents saved it, and plan_complete logs
// the numbers; a plan no agent worked on says nothing of it, and logs its numbers all the same.
func TestPlanCompleteSaysWhatPhaseAgentsSaved(t *testing.T) {
	pl := demoPlan()
	ref := state.WorkerRef("run", "P1")
	cases := []struct {
		name   string
		agent  bool   // P1 was a phase agent's
		say    string // the systemMessage
		fields map[string]any
	}{
		{name: "a plan with an agent", agent: true,
			say:    "baton: plan complete — all 2 phases done · phase agents saved about 1h50m: 4h00m of phase work in 2h10m",
			fields: map[string]any{"decisions": 0, "left": 0, "agent_phases": 1, "work_seconds": 14400, "wall_seconds": 7800, "saved_seconds": 6600}},
		{name: "a plan without agents",
			say:    "baton: plan complete — all 2 phases done",
			fields: map[string]any{"decisions": 0, "left": 0, "agent_phases": 0, "work_seconds": 15000, "wall_seconds": 7800, "saved_seconds": 7200}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := running() // P0 under way since tStop
			p1 := &state.PhaseState{Status: state.PhaseDone, StartedAt: tStop, DoneAt: tStop.Add(130 * time.Minute)}
			if c.agent {
				// Its agent worked from 0:05 to 2:05, and its work landed at 2:10.
				p1.Worker = ref
				st.Workers = []state.Worker{{Phase: "P1", Ref: ref, Status: state.WorkerLanded, Assigned: tStop,
					Started: tStop.Add(5 * time.Minute), Finished: tStop.Add(125 * time.Minute), Since: p1.DoneAt, Generation: st.Generation}}
			}
			st.Phases["P1"] = p1
			if _, err := state.Done(&st, pl, "P0", tStop.Add(2*time.Hour), state.DoneOpts{}); err != nil || st.Mode != state.ModeComplete {
				t.Fatalf("done: %v (mode %s)", err, st.Mode)
			}
			d := decideStop(&st, pl, tStop.Add(130*time.Minute), decide.Words{Git: true}, nil)
			if msg, _ := d.output["systemMessage"].(string); msg != c.say {
				t.Errorf("systemMessage %q, want %q", msg, c.say)
			}
			if n := st.Run.Notices; len(n) != 1 || n[0].Text != "Demo: "+strings.TrimPrefix(c.say, "baton: plan complete — ") {
				t.Errorf("notices %+v", n)
			}
			var logged map[string]any
			for _, e := range d.events {
				if e.kind == "plan_complete" {
					logged = e.fields
				}
			}
			if fmt.Sprint(logged) != fmt.Sprint(c.fields) {
				t.Errorf("plan_complete logged %v, want %v", logged, c.fields)
			}
		})
	}
}
