package loop

import (
	"errors"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Claude Code attaches the session's plan-mode file in full to the conversation after every compaction
// while it exists (spike 38). baton moves a plan it attaches from that file out of the way as it attaches
// it; a run attached before it did so (an earlier baton's) has its plan moved here, once, as the lead's
// next phase boundary compacts, before the compaction would attach the file once more (state.Aside).

// errLooked leaves a run whose plan another process looked at already as it is.
var errLooked = errors.New("looked at already")

// moveAside moves the run's plan file out of Claude Code's way as the compaction st has queued to start a
// phase (a boundary, a switch) is about to be asked for, if baton has not looked at it since the plan was
// attached (State.PlanLooked): when it is the session's plan-mode file (the slug from the lead's transcript, the
// one its hooks marked last) and no other live run's plan. It logs plan_moved, or plan_not_moved with why,
// and tells the human the plan's new path; briefs name it from here on, and the workers' sub-runs follow
// it (replan).
func (l *Loop) moveAside(st state.State) {
	if st.PlanLooked || !st.Run.Compaction.StartsPhase() || l.Store.Worker() != "" {
		return
	}
	pl, err := l.Store.LoadPlan()
	if err != nil {
		return
	}
	var transcript string
	if st.Run.Proven != nil {
		transcript = st.Run.Proven.Path
	}
	a := l.Store.ProjectOf().LookAside(l.Store.Ref, pl.File, transcript) // read before the lock
	file := pl.File
	var moved bool
	var unmoved error
	_, err = l.Store.Update(func(st *state.State) error {
		if st.PlanLooked {
			return errLooked
		}
		st.PlanLooked = true
		cur, err := l.Store.LoadPlan()
		if err != nil || cur.File != file {
			return nil // another plan since: it was looked at as it was attached
		}
		if moved, unmoved = a.Move(&cur); !moved {
			return nil
		}
		if err := l.Store.WritePlan(cur); err != nil {
			state.Unmove(&cur)
			moved, unmoved = false, err
			return nil
		}
		pl = cur
		st.Run.Announce = append(st.Run.Announce, decide.PlanMoved(cur.Origin, cur.File))
		return nil
	})
	switch {
	case errors.Is(err, errLooked):
	case err != nil:
		l.logf("loop: moving the plan out of Claude Code's way: %v", err)
	case moved:
		l.logf("loop: moved the plan %s to %s, out of Claude Code's way", pl.Origin, pl.File)
		l.Store.Event("plan_moved", map[string]any{"from": pl.Origin, "to": pl.File, "by": "boundary"})
	case unmoved != nil:
		l.Store.Event("plan_not_moved", map[string]any{"plan": file, "why": unmoved.Error(), "by": "boundary"})
	case a.PlanMode(file) && a.UsedBy != "":
		l.Store.Event("plan_not_moved", map[string]any{"plan": file, "why": "another live run's plan", "run": a.UsedBy, "by": "boundary"})
	}
}
