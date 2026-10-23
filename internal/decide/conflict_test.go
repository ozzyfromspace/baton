package decide

import (
	"fmt"
	"strings"
	"testing"
)

// A landing that conflicts tells its agent, under each path, the run's branch's commits that changed it
// since the two branches last met, newest first, with the phase whose landing brought each, at most 5 a
// path; with no history, it says what it always said: the merge is under way, to resolve and commit.
func TestLandConflictListsTheHistory(t *testing.T) {
	var many []ConflictCommit
	for i := 8; i >= 1; i-- {
		many = append(many, ConflictCommit{Hash: fmt.Sprintf("c%06d", i), Subject: fmt.Sprintf("change %d", i), Phase: "P4"})
	}
	const tail = "Resolve each conflict so that both your change and the one already on main keep working, git add the files, and git commit --no-edit to finish the merge. " +
		"The merge is already under way: never run git merge, rebase, reset or merge --abort, which would start another or undo the one in progress, " +
		"and a merge keeps every commit as it is. Run the phase's checks again, then run baton done P6 again."
	const started = "baton started merging main into your branch, in your worktree, and the merge stopped at those conflicts: it is still under way, with git's conflict markers in "
	for _, c := range []struct {
		name        string
		paths       []string
		history     map[string][]ConflictCommit
		wants, lack []string
	}{
		{"commits and phases", []string{"notes.md", "go.sum"}, map[string][]ConflictCommit{
			"notes.md": {{Hash: "1a2b3c4", Subject: "docs: P3's notes", Phase: "P3"}, {Hash: "5d6e7f8", Subject: "wip: the lead's", Phase: ""}},
			"go.sum":   {{Hash: "9a8b7c6", Subject: "build: bump", Phase: "P2"}},
		}, []string{
			"[baton] Your work on P6 did not land yet: other work landed on main, the run's branch, meanwhile, and it conflicts with yours in notes.md, go.sum. " +
				started + "those files. What main did to those files since your branch last met it, newest first (git show <hash> shows a commit), so you know whose change you merge with yours:\n" +
				"\nnotes.md:\n  1a2b3c4 docs: P3's notes (P3's work)\n  5d6e7f8 wip: the lead's (made in the lead's checkout)\n" +
				"\ngo.sum:\n  9a8b7c6 build: bump (P2's work)\n\n" + tail,
		}, nil},
		{"more than 5", []string{"a.go"}, map[string][]ConflictCommit{"a.go": many}, []string{
			"What main did to that file since",
			"\na.go:\n  c000008 change 8 (P4's work)\n  c000007 change 7 (P4's work)\n  c000006 change 6 (P4's work)\n  c000005 change 5 (P4's work)\n  c000004 change 4 (P4's work)\n  and 3 more\n\n" + tail,
		}, []string{"change 3"}},
		{"a path with none", []string{"a.go", "b.go"}, map[string][]ConflictCommit{"b.go": many[:1]}, []string{"\nb.go:\n  c000008 change 8 (P4's work)\n\n"}, []string{"a.go:"}},
		{"no history", []string{"a.go"}, nil, []string{
			"[baton] Your work on P6 did not land yet: other work landed on main, the run's branch, meanwhile, and it conflicts with yours in a.go. " + started + "that file. " + tail,
		}, []string{"What main did"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := LandConflict("P6", "main", c.paths, c.history)
			for _, want := range c.wants {
				if !strings.Contains(got, want) {
					t.Errorf("lacks %q:\n%s", want, got)
				}
			}
			for _, lack := range c.lack {
				if strings.Contains(got, lack) {
					t.Errorf("says %q:\n%s", lack, got)
				}
			}
		})
	}
}
