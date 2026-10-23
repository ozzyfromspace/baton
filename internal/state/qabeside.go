package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// QA phases run beside the lead (v0.8.15). A QA phase runs the plan's full checks, which take hours in a
// real run; as the lead's, it held the human's own checkout for all of them, where the human could not start
// on a review of their own (a real run: 3h20m and 9 runs). So where the run has phase agents (HasAgents),
// the lead passes over a QA phase that can start (Next), and a phase agent takes it (NextWorker), in a
// worktree of its own, even as the plan's last phase: the lead, holding none, idles as it does while agents
// hold every phase. A QA phase an agent of the plan failed on (it could not start, or baton took the phase
// back from an agent the human stopped twice) is the lead's, as every phase was before; so is one the lead
// started and set aside. With no agents (workers: 0, serial mode), nothing changes.
//
// Once every phase that is not a QA phase is done, the human hears that a review of theirs now reaches the
// QA phase in time (ReviewReady), once a plan.

// HasAgents reports a run whose phases go to phase agents beside the lead: its host runs them
// (Runtime.Fleet) and did not decide to run the phases one at a time in the lead (SerialWhy), and the run
// has a branch for their work to land on. A host that takes the run over records its own as it starts
// (loop's fleetOn): until then the last one's stands, so a lead that settles its run on the new host's
// first tick does not take up a QA phase an agent was about to, and one with no agents takes it up at its
// next settle.
func (st State) HasAgents() bool {
	return st.Run.Fleet != "" && st.SerialWhy() == "" && st.RunBranch != ""
}

// forAgent reports phase ph as one the lead passes over as it takes its next phase up (Next), for a phase
// agent to take (NextWorker): a QA phase, in a run with agents, that no agent of the plan attached now has
// failed on.
func (st State) forAgent(ph plan.Phase) bool {
	return ph.QA() && st.HasAgents() && !st.failedOn(ph.ID)
}

// failedOn reports a worker of the plan attached now that failed on phase: its phase was taken back from
// it (Unassign, ReleaseWorker).
func (st State) failedOn(phase string) bool {
	return slices.ContainsFunc(st.Workers, func(w Worker) bool {
		return w.Phase == phase && w.Status == WorkerFailed && w.Generation == st.Generation
	})
}

// ReviewTold is when the human was told that every phase but the QA phases was done (ReviewReady), and for
// which of the run's plans (State.Generation).
type ReviewTold struct {
	Generation int       `json:"generation"`
	At         time.Time `json:"at"`
}

// ReviewReady lists the QA phases of p that a review of the human's would reach now, or nil: once every
// phase of p that is not a QA phase is done (one at least), the QA phases not done whose session takes the
// human's words (`@<id>`): the lead holds it, or a phase agent of baton's is at work on it. None while the
// plan does not run, or once the human was told for this plan (Runtime.Review).
func ReviewReady(st State, p plan.Plan) []string {
	if st.Mode != ModeRunning || st.Run.Review != nil && st.Run.Review.Generation == st.Generation {
		return nil
	}
	others, left := 0, []string{}
	for _, ph := range p.Phases {
		ps := st.Phases[ph.ID]
		done := ps != nil && ps.Status == PhaseDone
		switch {
		case !ph.QA() && !done:
			return nil
		case !ph.QA():
			others++
		case !done:
			left = append(left, ph.ID)
		}
	}
	if others == 0 {
		return nil
	}
	var out []string
	for _, id := range left {
		if st.hears(id) {
			out = append(out, id)
		}
	}
	return out
}

// hears reports phase id, not done, as one whose session the human's words for it reach now: the lead's
// (in hand, or started there and verifying or set aside), or a phase agent's at work on it that baton's
// mod reaches (Addressee).
func (st State) hears(id string) bool {
	if w := st.WorkerOn(id); w != nil {
		return w.Status == WorkerWorking && w.AgentID != "" && w.Retiring.IsZero()
	}
	ps := st.Phases[id]
	return id == st.Current || ps != nil && (ps.Status == PhaseActive || ps.Status == PhaseVerifying || ps.Status == PhaseParked)
}

// ToldReview records that the human was told, at now, that this plan is ready for their review.
func ToldReview(st *State, now time.Time) {
	st.Run.Review = &ReviewTold{Generation: st.Generation, At: now}
}
