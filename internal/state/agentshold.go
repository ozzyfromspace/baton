package state

import (
	"slices"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The lead's stop with no phase in hand says what the run waits on. When nothing holds it but phase
// agents' work, "no phase can start yet" reads as if the run were stuck, or waited on the human, while its
// agents work: AgentsHold names the phases with agents instead, and what waits on them.

// AgentsHold is how phase agents' work alone holds a run whose lead holds no phase (HeldByAgents). AtWork
// is the phases at work in phase agents (workers starting, working or landing), in plan order, and Last
// says they are every phase left. Behind is the phases not started whose unmet After: phases are all
// among AtWork, in plan order, each with those it waits on. Its zero value holds nothing.
type AgentsHold struct {
	AtWork []string
	Last   bool
	Behind []Behind
}

// Behind is Phase, not started, and On, the phases at work in phase agents that it waits on, in plan
// order.
type Behind struct {
	Phase string
	On    []string
}

// Holds reports whether h holds the run: every phase left is with an agent, or a phase not started waits
// on agents' work alone.
func (h AgentsHold) Holds() bool { return h.Last || len(h.Behind) > 0 }

// Held lists the phases of h.Behind.
func (h AgentsHold) Held() []string {
	var out []string
	for _, b := range h.Behind {
		out = append(out, b.Phase)
	}
	return out
}

// HeldByAgents is what holds the run st, of plan p, on its phase agents' work, as its lead stops with no
// phase in hand. It is zero when anything else holds the run too, or instead: the plan does not run, the
// lead holds a phase, a job is queued or running, a gate runs in the working tree, the question about a
// phase whose agent the human stopped twice is open, or a phase can start (Next, NextWorker), held or
// not.
func HeldByAgents(st State, p plan.Plan) AgentsHold {
	if st.Mode != ModeRunning || st.Current != "" || len(st.Unfinished()) > 0 || st.HereGate() != nil || st.StoppedTwice() != nil ||
		Next(st, p) != "" || NextWorker(st, p) != "" {
		return AgentsHold{}
	}
	var h AgentsHold
	for _, ph := range p.Phases {
		if w := st.WorkerOn(ph.ID); w != nil && (w.Status == WorkerStarting || w.Status == WorkerWorking || w.Status == WorkerLanding) {
			h.AtWork = append(h.AtWork, ph.ID)
		}
	}
	if len(h.AtWork) == 0 {
		return AgentsHold{}
	}
	if slices.Equal(Remaining(st, p), h.AtWork) {
		h.Last = true
		return h
	}
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status != PhasePending {
			continue
		}
		var on []string
		unmet := 0
		for _, d := range p.Deps(ph.ID) {
			if ps := st.Phases[d]; ps == nil || ps.Status != PhaseDone {
				unmet++
				if slices.Contains(h.AtWork, d) {
					on = append(on, d)
				}
			}
		}
		if unmet > 0 && len(on) == unmet {
			slices.SortFunc(on, func(a, b string) int { return p.Index(a) - p.Index(b) })
			on = slices.Compact(on)
			h.Behind = append(h.Behind, Behind{Phase: ph.ID, On: on})
		}
	}
	if len(h.Behind) == 0 {
		return AgentsHold{}
	}
	return h
}
