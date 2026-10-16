package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// baton status says what phase agents saved a complete plan, and nothing of it before the plan is
// complete, or for a plan no agent worked on.
func TestStatusSaysWhatPhaseAgentsSaved(t *testing.T) {
	for _, c := range []struct {
		name     string
		complete bool
		agent    bool
		want     string // "" for no line
	}{
		{name: "complete, with an agent", complete: true, agent: true, want: "time: phase agents saved about 50m: 3h00m of phase work in 2h10m\n"},
		{name: "complete, without agents", complete: true},
		{name: "running, with an agent", agent: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := attached(t)
			t0, ref := s.now, state.WorkerRef(s.store().Ref, "P1")
			s.set(func(x *state.State) {
				// P0 the lead's for an hour; P1 an agent's, at work from 0:05 to 1:05, landed at 1:10; R1 the
				// lead's from 1:10 to 2:10.
				x.Phases["P0"] = &state.PhaseState{Status: state.PhaseDone, StartedAt: t0, DoneAt: t0.Add(time.Hour)}
				p1 := &state.PhaseState{Status: state.PhaseDone, StartedAt: t0, DoneAt: t0.Add(70 * time.Minute)}
				if c.agent {
					p1.Worker = ref
					x.Workers = []state.Worker{{Phase: "P1", Ref: p1.Worker, Status: state.WorkerLanded, Assigned: t0, Since: p1.DoneAt,
						Started: t0.Add(5 * time.Minute), Finished: t0.Add(65 * time.Minute), Generation: x.Generation}}
				}
				x.Phases["P1"] = p1
				x.Phases["R1"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: t0.Add(70 * time.Minute)}
				if c.complete {
					x.Phases["R1"].Status, x.Phases["R1"].DoneAt = state.PhaseDone, t0.Add(130*time.Minute)
					x.Mode, x.Current = state.ModeComplete, ""
				}
			})
			out := s.must("", "status")
			if c.want == "" && strings.Contains(out, "phase agents saved") {
				t.Errorf("status says what agents saved:\n%s", out)
			}
			if c.want != "" && !strings.Contains(out, c.want) {
				t.Errorf("status lacks %q:\n%s", c.want, out)
			}
		})
	}
}
