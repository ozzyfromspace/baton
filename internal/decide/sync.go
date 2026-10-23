package decide

import (
	"fmt"
	"strings"
)

// A phase agent never runs git merge, rebase, reset or push on its branch (WorkerIntro): in a real run,
// Claude Code's auto mode refused an agent's own `git merge` of the run's branch twice, and its phase
// waited 4h50m for the human. baton runs every merge itself, in the agent's worktree: as it lands the
// phase (loop/merge.go), and when the agent asks for the work landed on the run's branch with `baton sync`.
// A merge that stops on conflicts is left in progress for the agent, which resolves the files, adds them
// and commits; `baton done` commits it for the agent once nothing is unmerged. Nothing it is told to do
// rewrites or discards a commit.

// keptMergeNever is what an agent finishing a merge baton left in progress never runs.
const keptMergeNever = "The merge is already under way: never run git merge, rebase, reset or merge --abort."

// SyncLead refuses `baton sync` to the lead session, whose checkout is on the run's branch (branch, ""
// when unknown) itself.
func SyncLead(branch string) string {
	on := "the run's branch itself"
	if branch != "" {
		on = "the run's branch (" + branch + ") itself"
	}
	return fmt.Sprintf("not synced — baton sync is a phase agent's: it brings the work landed on the run's branch into the agent's own branch, in its worktree. "+
		"This is the lead session, whose checkout is on %s, where baton lands each phase's work: there is nothing to bring in. Carry on.", on)
}

// SyncNoBranch refuses `baton sync` to the agent on phase in a run with no branch baton lands phases on.
func SyncNoBranch(phase string) string {
	return fmt.Sprintf("not synced — this run has no branch that baton lands phases on, so there is nothing to bring into %s's branch. Carry on with %s.", phase, phase)
}

// SyncDirty refuses `baton sync` over uncommitted changes to tracked files (files, one per line): baton
// merges only into a worktree with none, so that a merge that stops can be undone exactly.
func SyncDirty(files string) string {
	return fmt.Sprintf("not synced — your worktree has uncommitted changes, and baton merges only into a worktree with none:\n%s\n"+
		"Commit them first, by path (as work in progress if they are not finished), then run baton sync again. Never stash, reset or discard them to make way.", files)
}

// SyncHere refuses `baton sync` while job runs in the agent's worktree (verify --here): a merge would
// change the files it checks.
func SyncHere(job string) string {
	return fmt.Sprintf("not synced — job %s runs in your worktree (verify --here), and a merge now would change the files it checks. Run baton sync once it ends.", job)
}

// SyncFailed says that baton could not merge branch, the run's branch, into the agent's, and changed
// nothing: why is what git said.
func SyncFailed(branch, why string) string {
	return fmt.Sprintf("not synced — baton could not merge %s, the run's branch, into your branch, and changed nothing (git said: %s). "+
		"If an untracked file of yours is in the way, commit it or move it, then run baton sync again; otherwise carry on with your phase: baton merges the run's branch in again as it lands your work.",
		branch, why)
}

// SyncUpToDate says that the agent's branch holds branch, the run's branch, at its tip (head) already.
func SyncUpToDate(branch, head string) string {
	return fmt.Sprintf("baton: your branch has everything on %s, the run's branch, already (its tip %s is in your branch): nothing to bring in.", branch, short7(head))
}

// Synced says what baton's merge of branch, the run's branch, brought into the agent's branch, now at
// head: commits (oldest first), the work of phases (the landings that brought them; none for the lead's
// own), changing files.
func Synced(branch, head string, commits, phases, files []string) string {
	short := shorts(commits)
	whose := ""
	if len(phases) > 0 {
		whose = fmt.Sprintf(", the work of %s", andList(phases))
	}
	more := ""
	if len(files) > maxFiles {
		more = fmt.Sprintf(" and %d more", len(files)-maxFiles)
		files = files[:maxFiles]
	}
	return fmt.Sprintf("baton: merged %s, the run's branch, into your branch (now %s): %d %s came in (%s)%s, changing %s%s. "+
		"Read those files again before you edit them. Never amend, reset, rebase or revert commits you did not make, baton's merge among them.",
		branch, short7(head), len(short), plural(len(short), "commit", "commits"), strings.Join(short, ", "), whose, strings.Join(files, ", "), more)
}

// SyncConflict tells the agent on phase that baton's merge of branch, the run's branch, into its branch
// stopped at conflicts in paths, and is left in progress for it to finish, with, under each path, the
// commits of branch that changed it since the two last met (history, newest first; conflict.go). It is
// said as a failure ("baton: " comes before it).
func SyncConflict(phase, branch string, paths []string, history map[string][]ConflictCommit) string {
	return fmt.Sprintf("merged %s, the run's branch, into your branch in your worktree, and it stopped at conflicts in %s. %s"+
		"Resolve each so that both your change and the one from %s keep working, git add the files, and git commit --no-edit. %s "+
		"Then run the checks of what the conflicts touched, and go on with %s.",
		branch, strings.Join(paths, ", "), conflictHistory(branch, paths, history), branch, keptMergeNever, phase)
}

// MergeUnfinished refuses verb ("not done", "not synced") while the merge baton left in progress in the
// agent's worktree has paths still unmerged, or conflict markers left in what is staged (markers,
// "path:line"), and says how to finish it: again is the command that commits it once nothing is unmerged
// ("done P3", "sync").
func MergeUnfinished(verb, again string, paths, markers []string) string {
	what := strings.Join(paths, ", ") + " still " + plural(len(paths), "conflicts", "conflict")
	if len(paths) == 0 {
		what = "conflict markers are left at " + strings.Join(markers, ", ") + ", in what is staged"
	}
	return fmt.Sprintf("%s — the merge baton started in your worktree is not finished: %s. Finish it in three steps: "+
		"resolve each file so that both changes keep working, with no conflict marker left; git add the files; "+
		"and git commit --no-edit, or run baton %s again, which commits the merge once nothing is unmerged. %s",
		verb, what, again, keptMergeNever)
}

// MergeNotCommitted refuses verb ("not done", "not synced") when the agent resolved and added every file
// of the merge in progress, but baton could not commit it (why: what went wrong); again is the command to
// run once it is committed.
func MergeNotCommitted(verb, again, why string) string {
	return fmt.Sprintf("%s — the merge in your worktree is resolved, but baton could not commit it (%s). "+
		"Commit it yourself with git commit --no-edit, degraded if need be (e.g. --no-gpg-sign if signing fails), then run baton %s again. %s",
		verb, why, again, keptMergeNever)
}

// MergeConcluded tells the agent that baton committed the merge it finished resolving, as head.
func MergeConcluded(head string) string {
	return fmt.Sprintf("baton: every file of the merge in your worktree was resolved and added, so baton committed the merge (%s).", short7(head))
}

// MergeInProgressBrief opens the brief of an agent on phase whose worktree holds a merge in progress:
// baton merged branch (the run's branch; "" when unknown) into the phase's branch, and it stopped at
// conflicts. unmerged are the paths still in conflict. The agent told of it may be one this one replaced.
func MergeInProgressBrief(phase, branch string, unmerged []string) string {
	what := "the run's branch"
	if branch != "" {
		what = branch + ", the run's branch,"
	}
	left := "Every conflict is resolved and added already: git commit --no-edit finishes it."
	if len(unmerged) > 0 {
		left = fmt.Sprintf("Still in conflict: %s. Resolve each so that both changes keep working, git add the files, and git commit --no-edit.", strings.Join(unmerged, ", "))
	}
	return fmt.Sprintf("A merge is in progress in your worktree: baton merged %s into %s's branch, and it stopped at conflicts, which an agent before you may have been told of. "+
		"Finish it before anything else. %s %s Then run the phase's checks again.", what, phase, left, keptMergeNever)
}

// SyncedLine tells the human, in the lead's session, that the agent on phase brought branch, the run's
// branch, into its branch with `baton sync`: commits came in, and its branch is now at head.
func SyncedLine(phase, branch string, commits int, head string) string {
	return fmt.Sprintf("%s's agent synced: merged %s into %s's branch (%d %s; now %s)", phase, branch, phase, commits, plural(commits, "commit", "commits"), short7(head))
}

// SyncConflictLine tells the human, in the lead's session, that the merge of branch the agent on phase
// asked for stopped at conflicts in paths, left for that agent to resolve.
func SyncConflictLine(phase, branch string, paths []string) string {
	return fmt.Sprintf("%s's agent synced: merging %s into %s's branch stopped at conflicts in %s, left for its agent to resolve", phase, branch, phase, strings.Join(paths, ", "))
}

// MergeConcludedLine tells the human, in the lead's session, that baton committed the merge the agent on
// phase resolved in its worktree, as head.
func MergeConcludedLine(phase, head string) string {
	return fmt.Sprintf("committed the merge %s's agent resolved in its worktree (now %s)", phase, short7(head))
}
