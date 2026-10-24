package loop

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// tellComplete leaves the lead of run st baton's message that phase's landing completed the plan pl
// (decide.PlanCompleteMail), under the run's lock: the lead holds no phase, so nothing of its own reports
// the end, and the message wakes it, idle (Loop.deliver), or comes at its stop, to sum the run up for the
// human. workers are the decisions of the run's phase agents, read before the lock.
func tellComplete(st *state.State, pl plan.Plan, phase string, saving state.Saving, workers []state.Decision, now time.Time) {
	ds := slices.Concat(st.Decisions, workers)
	slices.SortStableFunc(ds, func(a, b state.Decision) int { return a.At.Compare(b.At) })
	state.Tell(st, state.Message{Kind: state.MessageDone, Text: decide.PlanCompleteMail(phase, len(pl.Phases), saving, ds)}, now)
}
