package loop

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase reported done again while the merge baton left in its worktree is not committed goes back to
// its worker, told which paths still conflict (or, all resolved and added, to commit it): no new conflict
// on record, nothing aborted, nothing landed. Once the worker commits the merge, it lands.
func TestADoneOverAnUnfinishedMergeGoesBack(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		resolved bool // the worker resolved and added a.txt, and did not commit
		unmerged []string
	}{
		{"nothing resolved", false, []string{"a.txt"}},
		{"resolved, not committed", true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := landingCrew(t)
			leads := x.leadCommits(map[string]string{"a.txt": "the lead's\n"})
			old := x.finish("P1", map[string]string{"a.txt": "the worker's\n"}, false)
			x.tick(time.Second) // the merge stops on the conflict, and is kept
			w := x.worker("P1")
			if !gitx.Merging(ctx, w.Worktree) || x.state().Conflicts != 1 {
				t.Fatalf("no merge kept\n%s", x.events())
			}
			if c.resolved {
				gittest.Write(t, w.Worktree, "a.txt", "both\n")
				gittest.Git(t, w.Worktree, "add", "a.txt")
			}
			x.doneAgain("P1")
			x.tick(time.Second)

			sst, _ := x.sub("P1").Load()
			if w := x.worker("P1"); w.Status != state.WorkerWorking || w.Merge != nil || sst.Current != "P1" || sst.Phases["P1"].Status != state.PhaseActive {
				t.Fatalf("worker %+v; sub-run %s on %q\n%s", w, sst.Mode, sst.Current, x.events())
			}
			if len(sst.Inbox) != 1 || sst.Inbox[0].Kind != state.MessageLand || sst.Inbox[0].Text != decide.LandUnfinished("P1", c.unmerged) {
				t.Errorf("the worker's inbox: %+v", sst.Inbox)
			}
			if !gitx.Merging(ctx, w.Worktree) || gitx.Head(w.Worktree) != old || gitx.RevParse(w.Worktree, "MERGE_HEAD") != leads {
				t.Error("the merge in progress was aborted or committed")
			}
			if c.resolved && read(t, w.Worktree, "a.txt") != "both\n" {
				t.Errorf("the worker's resolution is gone: %q", read(t, w.Worktree, "a.txt"))
			}
			unmerged := `"unmerged":["a.txt"]`
			if !x.has("land_returned", `"why":"its merge is not finished"`) || x.has("land_returned", `"why":"its merge is not finished"`, `"paths"`) ||
				x.has("land_returned", `"why":"its merge is not finished"`, unmerged) == c.resolved {
				t.Errorf("events:\n%s", x.events())
			}
			if n := x.state().Conflicts; n != 1 || len(x.state().Landed) != 0 {
				t.Errorf("%d landings in a row conflicted, want the one; landed %+v", n, x.state().Landed)
			}
			if !strings.Contains(x.announced(), "P1's work did not land: its merge is not finished → its worker takes it up again") {
				t.Errorf("announced %q", x.announced())
			}

			// The worker commits the merge and reports the phase done again: it lands.
			resolved := x.resolve("P1", map[string]string{"a.txt": "both\n"})
			x.doneAgain("P1")
			x.tick(time.Second)
			x.tick(time.Second)
			if x.worker("P1").Status != state.WorkerLanded || read(t, x.repo, "a.txt") != "both\n" || gitx.Head(x.repo) != resolved || x.state().Conflicts != 0 {
				t.Fatalf("not landed once the merge was committed\n%s", x.events())
			}
			if n := strings.Count(x.events(), `"kind":"land_returned"`); n != 2 || slices.Contains([]string{old, leads}, gitx.Head(x.repo)) {
				t.Errorf("%d hand-backs\n%s", n, x.events())
			}
		})
	}
}
