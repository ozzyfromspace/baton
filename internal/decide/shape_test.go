package decide

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// chainIDs is a chain of n phases, P0 to P<n-1>.
func chainIDs(n int) []string {
	var out []string
	for i := range n {
		out = append(out, "P"+strconv.Itoa(i))
	}
	return out
}

// The human sees a plan's shape in one line as it is put to them: its phases, its longest chain and how
// many phases can be worked on at once. A long chain is cut short; a plan of one phase has no line.
func TestPlanShapeLine(t *testing.T) {
	for _, c := range []struct {
		name string
		s    plan.Shape
		want string
	}{
		{"this release's plan", plan.Shape{Phases: 12, Chain: []string{"P1", "P7", "P8", "P9", "P11"}, Width: 7},
			"baton: 12 phases · longest chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once"},
		{"a chain", plan.Shape{Phases: 3, Chain: chainIDs(3), Width: 1}, "baton: 3 phases · longest chain 3 (P0 → P1 → P2) · one at a time"},
		{"none waits", plan.Shape{Phases: 4, Chain: chainIDs(1), Width: 4}, "baton: 4 phases · no phase waits for another · up to 4 at once"},
		{"a chain of nine, in full", plan.Shape{Phases: 9, Chain: chainIDs(9), Width: 1},
			"baton: 9 phases · longest chain 9 (P0 → P1 → P2 → P3 → P4 → P5 → P6 → P7 → P8) · one at a time"},
		{"a long chain, cut short", plan.Shape{Phases: 57, Chain: chainIDs(42), Width: 9},
			"baton: 57 phases · longest chain 42 (P0 → P1 → P2 → P3 → … → P39 → P40 → P41) · up to 9 at once"},
		{"one phase", plan.Shape{Phases: 1, Chain: chainIDs(1), Width: 1}, ""},
		{"no phases", plan.Shape{}, ""},
	} {
		if got := PlanShape(c.s); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A plan that is mostly a chain is turned back once with its numbers, its whole chain and the shape
// rules, and the model may put it to the human as it is, saying why the chain is real.
func TestShapeCheckWords(t *testing.T) {
	long := plan.Shape{Phases: 12, Chain: chainIDs(11), Width: 2}
	got := ShapeCheck(long)
	for _, want := range []string{
		"[baton] Check this plan's shape too. Of its 12 phases, 11 form one chain, each waiting for the one before it: " +
			"P0 → P1 → P2 → P3 → P4 → P5 → P6 → P7 → P8 → P9 → P10. At most 2 phases can be worked on at once",
		"Check each link of that chain against baton's shape rules:\n\n" + ShapeRules + "\n\n",
		"1. A dependency is a need.", "5. Outside waits off the main chain.", "6. A small task can be one phase.",
		"you may call ExitPlanMode again with the phases as they are, adding to the plan (in its Standing rules) a line on why the chain is real",
		"baton asks this once per plan too.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ShapeCheck lacks %q:\n%s", want, got)
		}
	}
	if got := ShapeCheck(plan.Shape{Phases: 6, Chain: chainIDs(6), Width: 1}); !strings.Contains(got, ": P0 → P1 → P2 → P3 → P4 → P5. Only one phase can be worked on at a time, so") {
		t.Errorf("a pure chain: %s", got)
	}
	for _, c := range []struct {
		name    string
		flagged bool
		want    string
	}{
		{"not flagged", false, "baton: before you see the plan, Claude checks it against your other instructions (CLAUDE.md, memory) for anything that competes with it"},
		{"flagged", true, "baton: before you see the plan, Claude checks it against your other instructions (CLAUDE.md, memory) for anything that competes with it, " +
			"and checks its longest chain (11 of 12 phases) against baton's shape rules"},
	} {
		if got := PlanChecked(long, c.flagged); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
