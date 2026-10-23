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
