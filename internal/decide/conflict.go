package decide

import (
	"fmt"
	"strings"
)

// A landing that conflicts goes back to the phase's agent, which merges the run's branch in itself
// (LandConflict). It is told whose changes it is merging: under each path that conflicted, the commits of
// the run's branch that changed it since the two branches last met, and the phase whose landing brought
// each. A real run's lead, told of a conflict and not its agent, had the human carry a merge note to an
// agent that had already merged.

// ConflictCommit is a commit of the run's branch that changed a path a landing conflicted in.
type ConflictCommit struct {
	Hash    string // abbreviated
	Subject string
	Phase   string // the phase whose landing brought it; "" for a commit made in the lead's checkout
}

// conflictMax is how many commits a path lists, the newest; the rest are counted.
const conflictMax = 5

// conflictHistory lists, under each of paths, the commits of branch that changed it (history, newest
// first): "" when it has none for any.
func conflictHistory(branch string, paths []string, history map[string][]ConflictCommit) string {
	var s strings.Builder
	for _, p := range paths {
		cs := history[p]
		if len(cs) == 0 {
			continue
		}
		fmt.Fprintf(&s, "\n%s:\n", p)
		for i, c := range cs {
			if i == conflictMax {
				fmt.Fprintf(&s, "  and %d more\n", len(cs)-i)
				break
			}
			fmt.Fprintf(&s, "  %s %s (%s)\n", c.Hash, c.Subject, broughtBy(c.Phase))
		}
	}
	if s.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("What %s did to %s since your branch last met it, newest first (git show <hash> shows a commit), so you know whose change you merge with yours:\n%s\n",
		branch, plural(len(paths), "that file", "those files"), s.String())
}

// broughtBy says how a commit came onto the run's branch: with phase's landing, or in the lead's checkout.
func broughtBy(phase string) string {
	if phase == "" {
		return "made in the lead's checkout"
	}
	return phase + "'s work"
}

// A file that conflicts in a second landing of the run serializes the phases that change it: each landing
// must merge the last one's changes to it. The lead hears of it (FileConflicts) only while phases not
// started name it, which a replan can still give files of their own, and baton opens that replan itself
// at the lead's next phase boundary (state.ReplanConflict; package replan's Due): nobody asks the human for
// one.

// ReplanFile is a file in a second landing conflict, as a replan's brief names it: with the phases not
// started whose sections name it.
type ReplanFile struct {
	Path   string
	Phases []string
}

// conflictPurpose is what a replan a conflict cued is for, as its brief opens.
const conflictPurpose = "so the phases that name the files that conflicted stop colliding when they land"

// conflictEdit is what a replan a conflict cued may change in a phase not started, beyond a split.
const conflictEdit = "- reword a phase not started so it works on a file of its own (its steps name the file it changes), and its done-when where that needs it"

// conflictCause says why a conflict cued a replan: files, each with the phases not started that name it.
func conflictCause(files []ReplanFile) string {
	var each []string
	for _, f := range files {
		if len(f.Phases) == 0 {
			each = append(each, f.Path)
			continue
		}
		each = append(each, fmt.Sprintf("%s (named by %s)", f.Path, andList(f.Phases)))
	}
	return fmt.Sprintf("%s %s in a second landing of this run, and phases not started name %s. "+
		"One file that phases running at once each change serializes them: each landing must merge the last one's changes to it, and conflicts there. "+
		"Give each phase not started that names %s a file of its own, and have a QA phase merge them; "+
		"where one truly builds on another's change to it, have it wait for that one instead (shape rule 4).",
		andList(each), plural(len(files), "conflicted", "each conflicted"), plural(len(files), "it", "them"), plural(len(files), "it", "one"))
}

// conflictAlso is the conflict a replan of another kind settles as well, after its own cause: "" for none.
func conflictAlso(files []ReplanFile) string {
	if len(files) == 0 {
		return ""
	}
	return " Also: " + conflictCause(files)
}
