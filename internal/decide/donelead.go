package decide

import "fmt"

// The lead's `baton done` of a worker's phase records the phase landed when its work is on the run's
// branch already (by a merge the human or the lead made), and is refused otherwise.

// WorkerPhaseOpen refuses the lead's `baton done` of phase, a worker's whose branch has ahead commits (-1:
// the branch cannot be read) that are not on into, the run's branch.
func WorkerPhaseOpen(phase, branch, into string, ahead int) string {
	lands := fmt.Sprintf("baton marks %s done once its agent's own `baton done %s` lands %s", phase, phase, plural(ahead, "it", "them"))
	what := fmt.Sprintf("its branch %s has %d %s that %s not on %s, the run's branch", branch, ahead, plural(ahead, "commit", "commits"), plural(ahead, "is", "are"), into)
	if ahead < 0 {
		what, lands = fmt.Sprintf("baton cannot read its branch %s", branch), fmt.Sprintf("baton marks %s done once its agent's own `baton done %s` lands its work", phase, phase)
	}
	return fmt.Sprintf("not done — %s is a worker's phase, and %s: %s. If its work is finished some other way, merge %s into %s first (git merge, never a rebase), then run baton done %s again.",
		phase, what, lands, branch, into, phase)
}

// DoneByLead is the decision the lead's `baton done` of phase records once every commit of the worker's
// branch is on into, the run's branch (commits of its own): what baton did, and how to undo it.
func DoneByLead(phase, branch, into string, commits int) (what, undo string) {
	what = fmt.Sprintf("recorded %s as landed, on the lead's baton done: every commit of its worker's branch %s (%d of its own) was on %s already, "+
		"so its phase agent is retired, with what it had not committed saved as a snapshot first", phase, branch, commits, into)
	undo = fmt.Sprintf("if %s is not finished, add what it lacks to the plan file as a new phase, which baton then runs; baton status lists the snapshot of "+
		"what its agent had not committed, and git restore --source=<snapshot> --worktree -- . puts it back", phase)
	return what, undo
}

// DoneLanded is what the lead's `baton done` of phase says once it recorded the worker's work landed, as
// decision id.
func DoneLanded(id, phase, branch, into string) string {
	return fmt.Sprintf("baton: noted (%s): %s is done — every commit of its branch %s is on %s already, so baton records its work landed. "+
		"baton ends its phase agent and retires it: what it had not committed is saved as a snapshot first (baton status lists it), then its worktree and branch go. The human will be told.",
		id, phase, branch, into)
}
