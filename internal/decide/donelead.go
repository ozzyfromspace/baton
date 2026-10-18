package decide

import "fmt"

// The lead's `baton done` of a worker's phase records the phase landed when its work is on the run's
// branch already (by a merge the human or the lead made), and is refused otherwise.

// WorkerPhaseOpen refuses the lead's `baton done` of phase, a worker's whose branch has ahead commits (-1:
// the branch cannot be read) that are not on into, the run's branch. on is the branch the lead's checkout
// is on when that is not into, and lacks into's commits (the run could not follow it; "" otherwise).
func WorkerPhaseOpen(phase, branch, into, on string, ahead int) string {
	lands := fmt.Sprintf("baton marks %s done once its agent's own `baton done %s` lands %s", phase, phase, plural(ahead, "it", "them"))
	what := fmt.Sprintf("its branch %s has %d %s that %s not on %s, the run's branch", branch, ahead, plural(ahead, "commit", "commits"), plural(ahead, "is", "are"), into)
	if ahead < 0 {
		what, lands = fmt.Sprintf("baton cannot read its branch %s", branch), fmt.Sprintf("baton marks %s done once its agent's own `baton done %s` lands its work", phase, phase)
	}
	if on != "" {
		return fmt.Sprintf("not done — %s is a worker's phase, and %s: %s. Your checkout is on %s, which lacks commits of %s, so the run cannot follow it there: "+
			"merge %s into %s (git merge, never a rebase), and the run follows %s; then, if %s's work is finished some other way, merge %s into %s too, and run baton done %s again.",
			phase, what, lands, on, into, into, on, on, phase, branch, on, phase)
	}
	return fmt.Sprintf("not done — %s is a worker's phase, and %s: %s. If its work is finished some other way, merge %s into %s first (git merge, never a rebase), then run baton done %s again.",
		phase, what, lands, branch, into, phase)
}

// DoneByLead is the decision the lead's `baton done` of phase records once every commit of the worker's
// branch is on into, the run's branch (commits of its own): what baton did, and how to undo it. dirty says
// its agent left work uncommitted, which is saved as a snapshot as it is retired.
func DoneByLead(phase, branch, into string, commits int, dirty bool) (what, undo string) {
	what = fmt.Sprintf("recorded %s as landed, on the lead's baton done: every commit of its worker's branch %s (%d of its own) was on %s already, "+
		"so its phase agent is retired", phase, branch, commits, into)
	undo = fmt.Sprintf("if %s is not finished, add what it lacks to the plan file as a new phase, which baton then runs", phase)
	if dirty {
		what += ", with what it had not committed saved as a snapshot first"
		undo += "; baton status lists the snapshot of what its agent had not committed, and git restore --source=<snapshot> --worktree -- . puts it back"
	}
	return what, undo
}

// DoneLanded is what the lead's `baton done` of phase says once it recorded the worker's work landed, as
// decision id (dirty: as DoneByLead).
func DoneLanded(id, phase, branch, into string, dirty bool) string {
	retired := "baton ends its phase agent and retires it, and its worktree and branch go."
	if dirty {
		retired = "baton ends its phase agent and retires it: what it had not committed is saved as a snapshot first (baton status lists it), then its worktree and branch go."
	}
	return fmt.Sprintf("baton: noted (%s): %s is done — every commit of its branch %s is on %s already, so baton records its work landed. %s The human will be told.",
		id, phase, branch, into, retired)
}
