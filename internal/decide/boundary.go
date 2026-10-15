package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Named is how a sentence names the phase in hand: its id, or "the plan" while the lead holds none (every
// phase left is a phase agent's, or waits for one or for a job), so that no sentence has a blank for it.
func Named(phase string) string {
	if phase == "" {
		return "the plan"
	}
	return phase
}

// takenUp is what the boundary owed in st takes up, as a sentence says it: why, when more than a phase
// finishing led to it (a switch set the phase in hand aside, a verification did not pass), then the phase
// and how it comes next: "P4 begins", "P4 resumes", or "P20 reopens".
func takenUp(st state.State) (why, next string) {
	id := Named(st.Current)
	next = id + " begins"
	if c := st.Run.Compaction; c.InFlight() && c.Switch != nil && c.Switch.To == st.Current {
		why = fmt.Sprintf("baton set %s aside while its job %s runs, and ", c.Switch.From, c.Switch.Job)
	}
	ps := st.Phases[st.Current]
	if ps == nil || ps.Status != state.PhaseParked {
		return why, next
	}
	pk := ps.Park
	if pk == nil || pk.Reason != state.ParkRed && pk.Reason != state.ParkUnverified {
		return why, id + " resumes"
	}
	switch j := st.Job(pk.Job); {
	case j == nil || pk.Job == "":
		why += fmt.Sprintf("%s's verification is gone from the record, so ", id)
	case pk.Reason == state.ParkRed:
		why += fmt.Sprintf("%s's verification %s (job %s), so ", id, Outcome(*j), j.ID)
	default:
		why += fmt.Sprintf("%s's verification %s (job %s), with no verdict, so ", id, Outcome(*j), j.ID)
	}
	return why, id + " reopens"
}

// BoundaryHold is why the lead's calls are held while the boundary owed in st waits for the turn to end:
// what comes after the compaction ("P4 begins after baton compacts the context").
func BoundaryHold(st state.State) string {
	why, next := takenUp(st)
	return why + next + " after baton compacts the context"
}

// BoundaryWaits tells the model, once, in a turn the boundary baton queued itself does not cut short
// (state.State.BoundaryWaits), that the phase comes after this turn: the turn's own work goes on, but
// not the phase's, whose brief comes with the compaction.
func BoundaryWaits(st state.State) string {
	why, next := takenUp(st)
	return fmt.Sprintf("[baton] %s%s once this turn ends, after baton compacts the context: its brief comes then. "+
		"Finish what this turn is doing, but do not start on %s in it.", why, next, Named(st.Current))
}

// BoundaryWaitsSay is the human's line for the same: the turn under way runs to its end first.
func BoundaryWaitsSay(st state.State) string {
	_, next := takenUp(st)
	return "baton: this turn runs to its end; then baton compacts the context, and " + next
}
