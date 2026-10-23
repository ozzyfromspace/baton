package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A QA phase runs beside the lead where the run has phase agents (state.HasAgents), so that its hours of
// full checks never hold the human's own checkout: the host records in the run that it runs agents
// (fleetOn), the lead passes over a QA phase that can start (state.Next), and an agent takes it
// (state.NextWorker), even as the plan's last phase. Then the human hears when to review: once every phase
// but the QA phases is done, and a QA phase's session takes their words, the coordinator says that what
// they find now reaches the QA phase in time (reviewReady), once a plan: announced in the lead's session,
// pushed (review_ready) and logged. Nothing waits on them.

// reviewEvery spaces out looks at whether the plan is ready for the human's review: each reads the plan.
const reviewEvery = 5 * time.Second

// fleetOn records in run st, in s, whether this host runs phase agents beside the lead (Runtime.Fleet: its
// instance, or ""), as the run's scheduler reads it (state.HasAgents). It writes only when that changes.
func (c *Coordinator) fleetOn(s *state.Store, st state.State) {
	want := ""
	if c.Fleet != nil {
		want = s.Instance
	}
	if st.Run.Fleet == want {
		return
	}
	if _, err := s.Update(func(st *state.State) error { st.Run.Fleet = want; return nil }); err != nil {
		c.Lead.logf("loop: recording whether the run has phase agents: %v", err)
	}
}

// reviewReady tells the human, once a plan, that every phase of run st, in s, but its QA phases is done,
// once a QA phase's session takes their words (state.ReviewReady): in the lead's session, by a push
// (review_ready), and in the run's events. It looks every reviewEvery at most.
func (c *Coordinator) reviewReady(now time.Time, s *state.Store, st state.State) {
	if st.Mode != state.ModeRunning || st.Run.Review != nil && st.Run.Review.Generation == st.Generation || now.Sub(c.reviewAt) < reviewEvery {
		return
	}
	c.reviewAt = now
	pl, err := s.LoadPlan()
	if err != nil || len(state.ReviewReady(st, pl)) == 0 {
		return
	}
	var qa []string
	agents := false
	if _, err := s.Update(func(st *state.State) error {
		if qa = state.ReviewReady(*st, pl); len(qa) == 0 {
			return nil
		}
		state.ToldReview(st, now)
		st.Run.Announce = append(st.Run.Announce, decide.ReviewReadyLine(qa))
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "review_ready", Text: decide.ReviewReady(qa), At: now})
		agents = st.HasAgents()
		return nil
	}); err != nil || len(qa) == 0 {
		return
	}
	c.Lead.logf("loop: every phase but %v is done: told the human a review now reaches it in time", qa)
	s.Event("review_ready", map[string]any{"phases": qa, "agents": agents})
}

// qaPhase reports phase id of pl as a QA phase.
func qaPhase(pl plan.Plan, id string) bool {
	i := pl.Index(id)
	return i >= 0 && pl.Phases[i].QA()
}
