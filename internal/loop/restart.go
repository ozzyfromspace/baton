package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead restarts on a newer baton at a point that loses nothing (Loop.restartable), and the restart ends
// the run's workers with it: the host ends their sessions, suspends them, and execs the newer baton, whose
// coordinator resumes each one in its worktree (resume). So the restart also waits until every worker is
// at such a point, and from the moment a newer baton waits, no new worker starts (pending).

// restartHold says why the lead's restart must wait for the run's workers, or "" once it need not: each
// worker's session must have started, its turn be over and its screen quiet, with no dialog open, nothing
// of its own running inside its claude process, and no compaction under way. baton's jobs do not count:
// their runners outlive the restart.
func (c *Coordinator) restartHold(now time.Time) string {
	for _, w := range c.launched() {
		st, err := w.store.Load()
		if err != nil {
			return fmt.Sprintf("the worker on %s: %v", w.phase, err)
		}
		if why := workerBusy(st.Run, now, w.launched); why != "" {
			return "the worker on " + w.phase + " " + why
		}
	}
	return ""
}

// workerBusy says what a worker's session, launched at launched, is doing that a restart would lose, or
// "" if nothing.
func workerBusy(r state.Runtime, now, launched time.Time) string {
	switch {
	case r.TurnStarted.Before(launched):
		return "has not started yet"
	case r.TurnOpen || now.Sub(r.LastActivity) < workerQuiet:
		return "is at work"
	case r.Dialogs.AnyOpen():
		return "waits on a dialog"
	case r.Crons > 0 || len(r.BusyBackground()) > 0 || len(r.Watching()) > 0:
		return "has background work running"
	case r.Compaction.InFlight():
		return "is compacting"
	}
	return ""
}
