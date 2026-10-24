package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker's work lands on the run's branch under the lead session, which keeps working in the checkout
// (docs/research/workers.md). The lead is told what landed, since its git history moved under it: left
// untold, it amended a landed commit as if it were its own (spike 25). A worker whose work cannot land
// as it is gets its phase back, with why.

// maxFiles is how many changed files a landing names.
const maxFiles = 20

// Landed tells the lead what landed on branch (the run's branch) underneath it, and which of its own
// commits a landing was not verified with (Landing.Unverified), which its next gate checks with it, or,
// for a landing whose verification again was skipped (Landing.QA), the QA phase ahead.
func Landed(ls []state.Landing, branch string) string {
	var parts, gates, skipped []string
	head := ""
	for _, l := range ls {
		commits := shorts(l.Commits)
		switch {
		case len(l.Unverified) > 0 && l.QA != "":
			skipped = append(skipped, skippedGate(l))
		case len(l.Unverified) > 0:
			gates = append(gates, fmt.Sprintf("%s's gate passed on %s, before your %s %s", l.Phase, short7(l.Verified),
				plural(len(l.Unverified), "commit", "commits"), strings.Join(shorts(l.Unverified), ", ")))
		}
		files := l.Files
		more := ""
		if len(files) > maxFiles {
			more = fmt.Sprintf(" and %d more", len(files)-maxFiles)
			files = files[:maxFiles]
		}
		parts = append(parts, fmt.Sprintf("%s's work: %d %s %s, changing %s%s", l.Phase, len(commits), plural(len(commits), "commit", "commits"),
			strings.Join(commits, ", "), strings.Join(files, ", "), more))
		head = l.Head
	}
	unverified := ""
	if len(gates) > 0 {
		unverified = strings.Join(gates, "; ") + "; your next gate runs them together. "
	}
	for _, g := range skipped {
		unverified += g + " "
	}
	return fmt.Sprintf("[baton] While you worked, baton landed the work of another session on %s, underneath you: %s. A worker made %s, not you. "+
		"Your own uncommitted changes were not touched, and git diff still shows only yours. HEAD is now %s, so your own last commit may no longer be HEAD. "+
		"Read those files again before you edit them. %sNever amend, reset, rebase or revert commits you did not make; if your work seems to need that, stop and say so.",
		branch, strings.Join(parts, "; "), plural(len(ls), "it", "them"), short7(head), unverified)
}

// MergeMessage is the message of the commit that merges the run's branch (branch) into a phase's branch
// (phaseBranch) to land it, worded as git words its own.
func MergeMessage(branch, phaseBranch string) string {
	return fmt.Sprintf("Merge branch '%s' into %s", branch, phaseBranch)
}

// LandConflict tells a worker on phase that the run's branch (branch) conflicts with its branch in paths:
// baton's merge of branch into its branch, in its worktree, stopped there and is left in progress
// (gitx.MergeError.Kept), so it resolves the files and commits the merge, with, under each path, the
// commits of branch that changed it since the two last met (history, newest first; conflict.go). It never
// runs git merge itself, which auto mode may refuse, nor anything that rewrites a commit or discards a
// resolution.
func LandConflict(phase, branch string, paths []string, history map[string][]ConflictCommit) string {
	return fmt.Sprintf("[baton] Your work on %s did not land yet: other work landed on %s, the run's branch, meanwhile, and it conflicts with yours in %s. "+
		"baton started merging %s into your branch, in your worktree, and the merge stopped at those conflicts: it is still under way, with git's conflict markers in %s. %s%s",
		phase, branch, strings.Join(paths, ", "), branch, plural(len(paths), "that file", "those files"), conflictHistory(branch, paths, history), resolveMerge(phase, branch))
}

// LandUnfinished tells a worker on phase that it reported the phase done again while the merge baton
// started in its worktree (LandConflict) is not committed yet, with unmerged, the paths still in conflict
// (none when every one is resolved and added, and only the commit is left).
func LandUnfinished(phase string, unmerged []string) string {
	left := "Every conflict is resolved and added: finish the merge with git commit --no-edit. "
	if len(unmerged) > 0 {
		left = fmt.Sprintf("%s still %s. %s", strings.Join(unmerged, ", "), plural(len(unmerged), "conflicts", "conflict"), resolveSteps("the run's branch"))
	}
	return fmt.Sprintf("[baton] Your work on %s did not land yet: the merge baton started in your worktree is not committed yet, so there is nothing to land. %s%s",
		phase, left, underWay(phase))
}

// resolveMerge is how a worker on phase finishes the merge of branch baton left in progress in its
// worktree: resolve, add, commit, check, done; never a command that starts, undoes or rewrites a merge.
func resolveMerge(phase, branch string) string { return resolveSteps(branch) + underWay(phase) }

// resolveSteps are the steps that finish a merge of branch in progress.
func resolveSteps(branch string) string {
	return fmt.Sprintf("Resolve each conflict so that both your change and the one already on %s keep working, git add the files, and git commit --no-edit to finish the merge. ", branch)
}

// underWay says what never to run over a merge in progress, and what comes once it is committed.
func underWay(phase string) string {
	return fmt.Sprintf("The merge is already under way: never run git merge, rebase, reset or merge --abort, which would start another or undo the one in progress, "+
		"and a merge keeps every commit as it is. Run the phase's checks again, then run baton done %s again.", phase)
}

// LandRefused tells a worker on phase that baton could not merge branch (the run's branch) into its
// branch, for why (what git said; baton's merge starts only on a worktree with nothing uncommitted). It
// commits what is uncommitted and reports the phase done again: baton merges as it lands. No model is
// asked to run git merge, which auto mode may refuse.
func LandRefused(phase, branch, why string) string {
	return fmt.Sprintf("[baton] Your work on %s did not land yet: baton could not merge %s, the run's branch, into your branch (git said: %s). "+
		"Commit any uncommitted changes in your worktree by path (git add -- <paths>, then git commit), never discarding them, then run baton done %s again: "+
		"baton merges %s into your branch itself as it lands your work. Never run git merge, rebase or reset for it.", phase, branch, why, phase, branch)
}

// ReverifyNotes are the notes of the verification baton runs again on a worker's branch, with branch (the
// run's branch) merged in: why it runs again (other work landed there, or, byLead, only the checkout's
// own commits came), and where the phase stood (notes, the model's own, from the verification that
// passed). A worker whose phase it reopens resumes from them.
func ReverifyNotes(branch, notes string, byLead bool) string {
	since := "other work had landed on " + branch + " meanwhile"
	if byLead {
		since = "commits had been made on " + branch + " in the checkout meanwhile"
	}
	n := fmt.Sprintf("baton merged %s, the run's branch, into this phase's branch to land it, and verified it again there, since %s.", branch, since)
	if notes != "" {
		n += " Before that: " + notes
	}
	return n
}

// LandCommit asks the lead's model to commit its uncommitted changes to paths, which have held phase's
// work from landing on branch (the run's branch) for waited: baton never lands over them. It commits them
// by path, as they are; nothing it is asked to do discards them.
func LandCommit(phase, branch string, paths []string, waited time.Duration) string {
	them := plural(len(paths), "it", "them")
	return fmt.Sprintf("[baton] %s's work, finished by its phase agent, has waited %s to land on %s: it changes %s, where you have uncommitted changes in the checkout, "+
		"and baton never lands over them. Commit your changes to %s now, by path (git add -- %s, then git commit), as work in progress if they are not finished: "+
		"baton then lands %s's work, merged with yours. If one is a generated or ignored file that does not belong in a commit, move it out of the way instead. "+
		"Never stash, reset or discard your changes to make way.",
		phase, Span(waited), branch, strings.Join(paths, ", "), them, strings.Join(paths, " "), phase)
}

// LandNudged tells the human that baton asked the lead to commit its changes to paths, which have held
// phase's landing for waited.
func LandNudged(phase string, paths []string, waited time.Duration) string {
	return fmt.Sprintf("baton: %s has waited %s to land on the lead's uncommitted changes to %s → asked the lead to commit %s",
		phase, Span(waited), strings.Join(paths, ", "), plural(len(paths), "it", "them"))
}

// LandOffBranch is the notice pushed to the human when phase's work cannot land because the checkout is
// on the branch on ("" for a detached HEAD), which lacks commits of branch, the run's branch: the run
// follows only a branch that holds them.
func LandOffBranch(phase, on, branch string) string {
	where := fmt.Sprintf("on the branch %s, which lacks commits of the run's branch %s", on, branch)
	if on == "" {
		where = "on a detached HEAD"
	}
	return fmt.Sprintf("%s's work waits to land: the checkout is %s. The lead was told how to let it land; baton lands it once the checkout is back on %s, or on a branch that holds it", phase, where, branch)
}

// MailTyped is the line baton types to give an idle session the messages it has for it, which come
// with the prompt.
func MailTyped(kinds []string) string {
	for _, k := range kinds {
		switch k {
		case state.MessageLand:
			return "[baton] Your work did not land yet: baton's message, attached, says why and what to do."
		case state.MessageAnswer:
			return "[baton] Your question has its answer: baton's message, attached, says what it is and what to do."
		case state.MessageCommit:
			return "[baton] A phase's work waits to land on your uncommitted changes: baton's message, attached, says which files to commit."
		case state.MessageWords:
			return "[baton] The human wrote something for a phase's agent that did not reach it: baton's message, attached, has their words."
		case state.MessageBranch:
			return MailBranch
		case state.MessageDone:
			return MailDone
		}
	}
	return "[baton] baton has a message for you, attached."
}

// AgentMail is the message that wakes an idle phase agent for the messages baton has for it, which come
// as it ends the turn (no tool call needed, so no permission prompt in the way), or with its next tool
// call: Claude Code gives a resumed agent nothing of baton's hooks as it starts.
func AgentMail(kinds []string) string {
	const comes = " " + endTurnFor + "it to you then" + orToolCall
	for _, k := range kinds {
		switch k {
		case state.MessageLand:
			return "[baton] Your work did not land yet: baton's message says why and what to do." + comes
		case state.MessageAnswer:
			return "[baton] Your question has its answer." + comes
		case state.MessagePlan:
			return "[baton] The human changed the plan: baton's message has the new text for you." + comes
		}
	}
	return "[baton] baton has a message for you." + comes
}

// LandedRewriteRefused is why the lead may not run cmd: it would rewrite commits that landed from the
// run's workers (phases), which the lead did not make.
func LandedRewriteRefused(cmd string, phases, commits []string) string {
	short := shorts(commits)
	return fmt.Sprintf("[baton] Not run: `%s` would rewrite work baton landed on this branch from %s (%s %s), which you did not make. "+
		"Never amend, reset, rebase or revert commits you did not make. Make a new commit of your own instead; if your task really needs that history rewritten, stop and say so.",
		cmd, strings.Join(phases, ", "), plural(len(short), "commit", "commits"), strings.Join(short, ", "))
}

func shorts(commits []string) []string {
	var out []string
	for _, c := range commits {
		out = append(out, short7(c))
	}
	return out
}

func short7(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// endTurnFor and orToolCall frame what a phase agent woken by a message is told to do to be given what
// baton has for it: end the turn, which needs no permission, or make a tool call first.
const (
	endTurnFor = "End this turn, and baton gives "
	orToolCall = " (or with your next tool call, if you make one first)."
)
