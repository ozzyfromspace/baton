package decide

import "fmt"

// The run's branch follows the lead's checkout to a branch that holds its tip (state.FollowBranch), and
// the lead's model is told how to let work land when it moved to one that does not (state.TellBranch).

// BranchFollowed is announced to the human as the run's branch follows the checkout from one branch to
// another.
func BranchFollowed(from, to string) string {
	return fmt.Sprintf("baton: the lead's checkout moved to %s, which holds the run's branch %s → the run follows it: phases' work lands on %s, and new phase agents start from it", to, from, to)
}

// BranchFollowedNote is what the lead's model hears at its next hook of the run's branch following its
// checkout's, from one branch to another. It asks nothing of it.
func BranchFollowedNote(from, to string) string {
	return fmt.Sprintf("[baton] The run's branch is now %s, where your checkout is: it holds everything %s had. Phases' work lands on %s from now on, "+
		"and new phase agents start from it. Nothing to do: carry on.", to, from, to)
}

// BranchUnfollowed tells the lead's model that phase's work waits to land on its checkout, which is on the
// branch on ("" for a detached HEAD) that lacks commits of run, the run's branch, and how to let it land.
func BranchUnfollowed(phase, on, run string) string {
	if on == "" {
		return fmt.Sprintf("[baton] %s's work waits to land: your checkout is on a detached HEAD, where baton lands nothing. To let it land, check out a branch: "+
			"%s, the run's branch, or one that holds all of %s, which the run then follows.", phase, run, run)
	}
	return fmt.Sprintf("[baton] %s's work waits to land: your checkout is on the branch %s, which lacks commits of the run's branch %s, so baton lands nothing there. "+
		"To let it land, merge %s into %s (git merge %s, never a rebase): the run then follows %s, and phases' work lands on it. Or check out %s again.",
		phase, on, run, run, on, run, on, run)
}

// MailBranch is the line typed to give an idle lead BranchUnfollowed.
const MailBranch = "[baton] A phase's work waits to land on your checkout's branch: baton's message, attached, says what to do."
