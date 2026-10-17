package hooks

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// chainDoc is a plan of n phases, each following the one before it.
func chainDoc(n int) string {
	var b strings.Builder
	b.WriteString("# A long road\n\n## Standing rules\nBe careful.\n")
	for i := range n {
		fmt.Fprintf(&b, "\n## P%d — Step %d\nDo it.\n", i, i)
	}
	b.WriteString("\n## Verification\nAll green.\n")
	return b.String()
}

// exitPlan plays the lead's model calling ExitPlanMode with doc as the plan in file: the hook's decision,
// its reason and what it shows the human.
func (f *fixture) exitPlan(file, doc string) (decision, why, shown string) {
	out := f.fire("PreToolUse", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": doc, "planFilePath": file}})
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	decision, _ = hso["permissionDecision"].(string)
	why, _ = hso["permissionDecisionReason"].(string)
	return decision, why, say(out)
}

// A plan that is mostly a chain is turned back once, in the same turn-back as the competing directives,
// with its chain and the shape rules; when it then goes to the human, they see its shape in one line.
func TestAMostlyChainPlanIsAskedAboutItsShapeOnce(t *testing.T) {
	f := newFixture(t, false)
	doc := chainDoc(7)
	d, why, shown := f.exitPlan("/plans/road.md", doc)
	if d != "deny" || !strings.HasPrefix(why, gatesAsked(t, doc)+"\n\n[baton] Check this plan's shape too.") ||
		!strings.Contains(why, "Of its 7 phases, 7 form one chain, each waiting for the one before it: P0 → P1 → P2 → P3 → P4 → P5 → P6.") ||
		!strings.Contains(why, decide.ShapeRules) || !strings.Contains(why, "call ExitPlanMode again with the phases as they are") {
		t.Fatalf("first call: %q\n%s", d, why)
	}
	if !strings.Contains(shown, ", its gates and QA phases against baton's gate rules, and its longest chain (7 of 7 phases) against baton's shape rules") {
		t.Errorf("showed %q", shown)
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/road.md", "phases": 7.0, "longest_chain": 7.0, "width": 1.0, "shape_flagged": true, "gates": true, "qa_phases": 0.0}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	// Resubmitted unchanged, it goes to the human, with its shape.
	d, _, shown = f.exitPlan("/plans/road.md", doc)
	if d == "deny" || shown != "baton: 7 phases · longest chain 7 (P0 → P1 → P2 → P3 → P4 → P5 → P6) · one at a time" {
		t.Fatalf("second call: %q, showed %q", d, shown)
	}
	if !f.logged("plan_shape", map[string]any{"plan": "/plans/road.md", "phases": 7.0, "longest_chain": 7.0, "width": 1.0, "mostly_chain": true}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	// Revised to run wider, it shows its new shape, and is not asked again.
	wider := strings.Replace(doc, "## P3 — Step 3\n", "## P3 — Step 3\nAfter: none\n", 1)
	d, _, shown = f.exitPlan("/plans/road.md", wider)
	if d == "deny" || shown != "baton: 7 phases · longest chain 4 (P3 → P4 → P5 → P6) · up to 2 at once" {
		t.Fatalf("revised: %q, showed %q", d, shown)
	}
}

// A plan that is not mostly a chain is turned back for the competing directives only, with its numbers
// logged; a plan of one phase, or one whose dependencies do not build, shows no shape line.
func TestAPlansShapeWithoutTheShapeCheck(t *testing.T) {
	f := newFixture(t, false)
	d, why, shown := f.exitPlan("/plans/a.md", approvedDoc)
	if d != "deny" || why != gatesAsked(t, approvedDoc) || strings.Contains(shown, "shape") {
		t.Fatalf("first call: %q %q, showed %q", d, why, shown)
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/a.md", "phases": 2.0, "longest_chain": 2.0, "width": 1.0, "shape_flagged": false}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if d, _, shown := f.exitPlan("/plans/a.md", approvedDoc); d == "deny" || shown != "baton: 2 phases · longest chain 2 (P0 → P1) · one at a time" {
		t.Errorf("second call: %q, showed %q", d, shown)
	}

	// Six phases, three at once: short of a chain, so only the numbers.
	wide := chainDoc(6)
	for _, id := range []string{"P2", "P4"} {
		wide = strings.Replace(wide, "## "+id+" — Step "+id[1:]+"\n", "## "+id+" — Step "+id[1:]+"\nAfter: none\n", 1)
	}
	if d, why, _ := f.exitPlan("/plans/wide.md", wide); d != "deny" || strings.Contains(why, "Check this plan's shape") {
		t.Errorf("a wide plan: %q\n%s", d, why)
	}
	if _, _, shown := f.exitPlan("/plans/wide.md", wide); shown != "baton: 6 phases · longest chain 2 (P0 → P1) · up to 3 at once" {
		t.Errorf("a wide plan showed %q", shown)
	}

	// One phase: whether or not baton asks about it, it has no shape to show.
	one := chainDoc(1)
	for range 2 {
		if _, _, shown := f.exitPlan("/plans/one.md", one); strings.Contains(shown, " · ") {
			t.Errorf("a one-phase plan showed %q", shown)
		}
	}

	// Dependencies that do not build: turned back for that first (unbuilt); put again as it is, asked
	// about the directives all the same, with no shape.
	broken := strings.Replace(chainDoc(7), "## P1 — Step 1\n", "## P1 — Step 1\nAfter: P4\n", 1)
	if d, why, _ := f.exitPlan("/plans/broken.md", broken); d != "deny" || !strings.Contains(why, "could not attach this plan as written") {
		t.Errorf("a plan that does not build: %q\n%s", d, why)
	}
	if d, why, _ := f.exitPlan("/plans/broken.md", broken); d != "deny" || why != decide.CompetingDirectives()+"\n\n"+decide.GateCheck(plan.Plan{}) {
		t.Errorf("a plan that does not build: %q\n%s", d, why)
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/broken.md", "gates": true}) || f.logged("directives_asked", map[string]any{"plan": "/plans/broken.md", "shape_flagged": false}) ||
		f.logged("directives_asked", map[string]any{"plan": "/plans/broken.md", "qa_phases": 0.0}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if d, _, shown := f.exitPlan("/plans/broken.md", broken); d == "deny" || shown != "" {
		t.Errorf("a plan that does not build, going to the human: %q, showed %q", d, shown)
	}
	for _, e := range f.events() {
		if e["kind"] == "plan_shape" && (e["plan"] == "/plans/one.md" || e["plan"] == "/plans/broken.md") {
			t.Errorf("logged %v", e)
		}
	}
}

// A plan held back by one of baton's holds does not go to the human, so it shows no shape.
func TestAHeldPlanShowsNoShape(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error { st.BoundaryOwed = true; return nil })
	f.exitPlan("/plans/a.md", approvedDoc)
	if d, why, shown := f.exitPlan("/plans/a.md", approvedDoc); d != "deny" || !strings.Contains(why, "End your turn now") || shown != "" {
		t.Errorf("held: %q %q, showed %q", d, why, shown)
	}
	for _, e := range f.events() {
		if e["kind"] == "plan_shape" {
			t.Errorf("logged %v", e)
		}
	}
}
