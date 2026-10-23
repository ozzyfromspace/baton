package replan

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A file in a second landing conflict of the run that phases not started name in their sections cues a
// replan of baton's own (state.ReplanConflict), once a file, at the lead's next phase boundary (Due): one
// file that phases running at once each change serializes them, since each landing must merge the last
// one's changes to it. The merge queue finds the phases that name it (Naming) as the conflict comes, for
// its look-in and the cue (state.CueConflict); the replan's brief names them again from the plan as it
// reads then.

// Naming is, for each of paths (relative to the repository's root), the phases among ids whose sections in
// doc, the text of pl, name it (plan.Paths), in the plan's order: a path is named as written, or by its
// end from a slash on (`loop/merge.go` names internal/loop/merge.go), as plans shorten paths. A path none
// names has no entry, and a phase whose section doc lacks names none.
func Naming(pl plan.Plan, doc []byte, ids, paths []string) map[string][]string {
	out := map[string][]string{}
	if len(paths) == 0 {
		return out
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for i, ph := range pl.Phases {
		if !want[ph.ID] {
			continue
		}
		sec, err := pl.Section(doc, i)
		if err != nil {
			continue
		}
		named := plan.Paths(sec)
		for _, p := range paths {
			for _, n := range named {
				if namesPath(n, p) {
					out[p] = append(out[p], ph.ID)
					break
				}
			}
		}
	}
	return out
}

// namesPath reports that n, a path a plan's section names, names p, a path in the repository: p itself, or
// its end from a slash on. A path from the root (/…) names nothing in the repository.
func namesPath(n, p string) bool {
	return !strings.HasPrefix(n, "/") && (n == p || strings.HasSuffix(p, "/"+n))
}
