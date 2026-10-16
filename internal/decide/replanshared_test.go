package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A replan's brief lists the phases not started that may run at once and name the same files, as
// information, after their shape and before the shape rules; with none, it says nothing of them. A long
// list shows its first sharedMax pairs and counts the rest.
func TestReplanBriefSharedFiles(t *testing.T) {
	const head = "Phases not started that may run at once and name the same files (for your information: phases that run at once and change the same file " +
		"collide when they land, and the later one has to merge the other's change; shape rule 4 says how to weigh it):\n"
	base := ReplanBrief{Title: "Files", By: state.ReplanHuman, Draft: "/r/draft.md", Max: 20 * time.Minute,
		Pending: []ReplanPhase{{ID: "P2", Title: "CLI", Waits: "needs no other phase"}, {ID: "P3", Title: "Docs", Waits: "needs no other phase"},
			{ID: "P4", Title: "Web", Waits: "needs no other phase"}},
		Shape: plan.Shape{Phases: 3, Chain: []string{"P2"}, Width: 3}}
	many := make([]plan.Overlap, sharedMax+3)
	for i := range many {
		many[i] = plan.Overlap{A: fmt.Sprintf("P%d", i), B: fmt.Sprintf("P%d", i+1), Paths: []string{"go.mod"}}
	}
	for _, c := range []struct {
		name        string
		shared      []plan.Overlap
		wants, lack []string
	}{
		{"none", nil, nil, []string{"name the same files"}},
		{"two pairs", []plan.Overlap{
			{A: "P2", B: "P3", Paths: []string{"internal/decide/words.go", "CLAUDE.md"}},
			{A: "P2", B: "P4", Paths: []string{"go.mod"}},
		}, []string{
			"up to 3 at once.\n" + head + "  P2 and P3: internal/decide/words.go, CLAUDE.md\n  P2 and P4: go.mod\n\nbaton's shape rules:\n",
		}, []string{"more pair"}},
		{"more than the brief lists", many, []string{
			"  P0 and P1: go.mod\n",
			fmt.Sprintf("  P%d and P%d: go.mod\n  and 3 more pairs\n\nbaton's shape rules:", sharedMax-1, sharedMax),
		}, []string{fmt.Sprintf("  P%d and P%d", sharedMax, sharedMax+1)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := base
			b.Shared = c.shared
			brief := b.Brief()
			for _, want := range c.wants {
				if !strings.Contains(brief, want) {
					t.Errorf("the brief lacks %q:\n%s", want, brief)
				}
			}
			for _, lack := range c.lack {
				if strings.Contains(brief, lack) {
					t.Errorf("the brief says %q:\n%s", lack, brief)
				}
			}
		})
	}
	if got := sharedFiles(many[:sharedMax+1]); !strings.HasSuffix(got, "  and 1 more pair\n") {
		t.Errorf("one more pair: %q", got)
	}
}
