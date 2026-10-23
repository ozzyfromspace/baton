package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// conflictDoc is newStill's plan as a document: P0 (in hand) and P1 (its agent's) name docs/notes.md and
// go.sum, and P2, not started, names docs/notes.md by its end.
const conflictDoc = "# Demo\n\n## P0 — zero\n\nAfter: none\n\nEdit `docs/notes.md`.\n\n## P1 — one\n\nAfter: none\n\n" +
	"Edit `docs/notes.md` and `go.sum`.\n\n## P2 — two\n\nAfter: P1\n\nAdd to `notes.md`.\n"

// planOf gives the run s the plan doc, written to a file of its own, as the human approved it.
func planOf(t *testing.T, s *state.Store, doc string) plan.Plan {
	t.Helper()
	file := filepath.Join(t.TempDir(), "demo.md")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build(file, []byte(doc), plan.Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(pl); err != nil {
		t.Fatal(err)
	}
	return pl
}

// A landing that conflicts goes back to its agent with the run's branch's commits that changed each
// conflicting file since its branch last met it, newest first: one that a phase's landing brought names
// that phase, one made in the checkout says so, and one older than the merge base is left out.
func TestAConflictIsToldWithItsHistory(t *testing.T) {
	x := landingCrew(t) // P1's branch starts at the repository's first commit, which made a.txt
	first := gitx.Head(x.repo)
	gittest.Write(t, x.repo, "a.txt", "P2's\n")
	gittest.Commit(t, x.repo, "feat: P2's change", "a.txt")
	landed := gitx.Head(x.repo)
	x.lead.Update(func(st *state.State) error {
		st.Landed = append(st.Landed, state.Landing{Phase: "P2", Commits: []string{landed}})
		return nil
	})
	gittest.Write(t, x.repo, "a.txt", "the lead's\n")
	gittest.Write(t, x.repo, "b.txt", "the lead's\n")
	gittest.Commit(t, x.repo, "wip: the lead's change", "a.txt", "b.txt")
	leads := gitx.Head(x.repo)
	x.finish("P1", map[string]string{"a.txt": "P1's\n"}, false)
	x.tick(time.Second)

	sst, _ := x.sub("P1").Load()
	if len(sst.Inbox) != 1 {
		t.Fatalf("the agent's inbox: %+v\n%s", sst.Inbox, x.events())
	}
	text := sst.Inbox[0].Text
	want := "it conflicts with yours in a.txt. What main did to that file since your branch last met it, newest first (git show <hash> shows a commit), " +
		"so you know whose change you merge with yours:\n\na.txt:\n  " + leads[:7] + " wip: the lead's change (made in the lead's checkout)\n  " +
		landed[:7] + " feat: P2's change (P2's work)\n\nMerge it into your branch yourself: git merge main"
	if !strings.Contains(text, want) || strings.Contains(text, first[:7]) {
		t.Errorf("told:\n%s\nwant it to hold:\n%s", text, want)
	}
}
