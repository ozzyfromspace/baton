package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker's work lands on the run's branch under the lead session, which keeps working in the checkout
// (docs/research/workers.md). The lead is told what landed, since its git history moved under it: left
// untold, it amended a landed commit as if it were its own (spike 25). A worker whose work cannot land
// as it is gets its phase back, with why.

// maxFiles is how many changed files a landing names.
const maxFiles = 20

// Landed tells the lead what landed on branch (the run's branch) underneath it.
func Landed(ls []state.Landing, branch string) string {
	var parts []string
	head := ""
	for _, l := range ls {
		var commits []string
		for _, c := range l.Commits {
			commits = append(commits, short7(c))
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
	return fmt.Sprintf("[baton] While you worked, baton landed the work of another session on %s, underneath you: %s. A worker made %s, not you. "+
		"Your own uncommitted changes were not touched, and git diff still shows only yours. HEAD is now %s, so your own last commit may no longer be HEAD. "+
		"Read those files again before you edit them. Never amend, reset, rebase or revert commits you did not make; if your work seems to need that, stop and say so.",
		branch, strings.Join(parts, "; "), plural(len(ls), "it", "them"), short7(head))
}

// LandConflict tells a worker on phase that its branch conflicts with branch (the run's branch) in
// paths, so it rebases and resolves it itself.
func LandConflict(phase, branch string, paths []string) string {
	return fmt.Sprintf("[baton] Your work on %s did not land yet: rebasing your branch onto %s, the run's branch, conflicts in %s, where other work landed meanwhile. "+
		"This time, rebase it yourself: git rebase %s, resolve each conflict so that both your change and the one already on %s keep working, git add the files, "+
		"and git rebase --continue. Run the phase's checks again, then run baton done %s again.",
		phase, branch, strings.Join(paths, ", "), branch, branch, phase)
}

// LandRefused tells a worker on phase that baton could not rebase its branch onto branch (the run's
// branch), for why (what git said), so it does it itself.
func LandRefused(phase, branch, why string) string {
	return fmt.Sprintf("[baton] Your work on %s did not land yet: baton could not rebase your branch onto %s, the run's branch (git said: %s). "+
		"Commit or remove any uncommitted changes in your worktree, then rebase it yourself: git rebase %s, resolving any conflicts. "+
		"Run the phase's checks again, then run baton done %s again.", phase, branch, why, branch, phase)
}

// ReverifyNotes are the notes of the verification baton runs again on a worker's branch, rebased onto
// branch (the run's branch): why it runs again, and where the phase stood (notes, the model's own, from
// the verification that passed). A worker whose phase it reopens resumes from them.
func ReverifyNotes(branch, notes string) string {
	n := fmt.Sprintf("baton rebased this phase's branch onto %s, the run's branch, to land it, and verified it again there, since other work had landed on %s meanwhile.", branch, branch)
	if notes != "" {
		n += " Before that: " + notes
	}
	return n
}

// MailTyped is the line baton types to give an idle session the messages it has for it, which come
// with the prompt.
func MailTyped(kinds []string) string {
	for _, k := range kinds {
		switch k {
		case state.MessageLand:
			return "[baton] Your work did not land yet: baton's message, attached, says why and what to do."
		case state.MessageAnswer:
			return "[baton] The human answered your question: baton's message, attached, says what to do."
		}
	}
	return "[baton] baton has a message for you, attached."
}

// LandedRewriteRefused is why the lead may not run cmd: it would rewrite commits that landed from the
// run's workers (phases), which the lead did not make.
func LandedRewriteRefused(cmd string, phases, commits []string) string {
	var short []string
	for _, c := range commits {
		short = append(short, short7(c))
	}
	return fmt.Sprintf("[baton] Not run: `%s` would rewrite work baton landed on this branch from %s (%s %s), which you did not make. "+
		"Never amend, reset, rebase or revert commits you did not make. Make a new commit of your own instead; if your task really needs that history rewritten, stop and say so.",
		cmd, strings.Join(phases, ", "), plural(len(short), "commit", "commits"), strings.Join(short, ", "))
}

func short7(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
