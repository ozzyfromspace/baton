package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// runWait is what the rest of the run st does while r, a phase agent's block, waits on the human, for its
// question (decide.RelayQuestion), as of now: whether nothing else in the run can move (state.State.Moves,
// with the run's plan), and the phases not done that wait on r's, directly or not. moves says what can
// move, for the log ("" when nothing can). Any other question says nothing of the rest of the run, nor
// does a block whose plan cannot be read: its question keeps saying the rest goes on.
func runWait(s *state.Store, st state.State, r state.Relay, now time.Time) (w decide.RunWait, moves string) {
	if r.Kind != state.RelayBlocked {
		return decide.RunWait{}, ""
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return decide.RunWait{}, "its plan cannot be read: " + err.Error()
	}
	if moves = st.Moves(pl, r.Phase, now); moves != "" {
		return decide.RunWait{}, moves
	}
	w.Stuck = true
	for _, id := range pl.Descendants(r.Phase) {
		if ps := st.Phases[id]; ps == nil || ps.Status != state.PhaseDone {
			w.Behind = append(w.Behind, id)
		}
	}
	return w, ""
}
