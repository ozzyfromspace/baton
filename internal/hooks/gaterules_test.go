package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
)

// gatesAsked is baton's turn-back at the first ExitPlanMode of doc, a plan that builds and is not mostly
// a chain: the competing directives, then the gate rules with its QA phases.
func gatesAsked(t *testing.T, doc string) string {
	t.Helper()
	pl, err := plan.Build("", []byte(doc), plan.Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	return decide.CompetingDirectives() + "\n\n" + decide.GateCheck(pl)
}

// qaDoc is a plan of eight phases with a QA phase after every three, each `After:` the three it checks:
// wide enough not to be mostly a chain.
const qaDoc = "# Checked often\n\n## Standing rules\nBe careful.\n\n" +
	"## P0 — Parser\nAfter: none\nParse.\n\n## P1 — Printer\nAfter: none\nPrint.\n\n## P2 — Lexer\nAfter: none\nLex.\n\n" +
	"## P3 — Check them\nAfter: P0, P1, P2\nType: qa\nCheck.\n\n" +
	"## P4 — Linker\nAfter: P3\nLink.\n\n## P5 — Loader\nAfter: P3\nLoad.\n\n## P6 — Runner\nAfter: P3\nRun.\n\n" +
	"## P7 — Check it all\nAfter: all\nType: qa\nCheck all.\n\n## Verification\nAll green.\n"

// Every plan's first ExitPlanMode is turned back with the gate rules beside the competing directives,
// listing its QA phases, each with the phases it covers since the one before; the QA phases before the
// last are flagged, and logged as extra_qa. The same plan put again goes to the human, asked nothing
// more: the rules are asked once per plan.
func TestTheGateRulesAreAskedOncePerPlan(t *testing.T) {
	f := newFixture(t, false)
	d, why, shown := f.exitPlan("/plans/qa.md", qaDoc)
	if d != "deny" || why != gatesAsked(t, qaDoc) {
		t.Fatalf("first call: %q\n%s", d, why)
	}
	for _, want := range []string{
		"[baton] Check this plan's gates too:",
		decide.GateRules,
		"- P3 (Check them): 3 phases since the plan's start\n- P7 (Check it all): 3 phases since P3\n",
		" P3 comes before the last QA phase: a round of full checks mid-plan costs the human time, worth it only where such a group ends.",
		"that is your judgment, not a number",
	} {
		if !strings.Contains(why, want) {
			t.Errorf("the turn-back lacks %q:\n%s", want, why)
		}
	}
	if !strings.Contains(shown, "and checks its gates and QA phases against baton's gate rules") {
		t.Errorf("showed %q", shown)
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/qa.md", "gates": true, "qa_phases": 2.0, "extra_qa": 1.0, "phases": 8.0, "shape_flagged": false}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	// Put again, changed or not, it goes to the human.
	if d, why, _ := f.exitPlan("/plans/qa.md", qaDoc+"\nRevised.\n"); d == "deny" || strings.Contains(why, "gate rules") {
		t.Fatalf("second call: %q\n%s", d, why)
	}
	asked := 0
	for _, e := range f.events() {
		if e["kind"] == "directives_asked" {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("directives_asked %d times:\n%s", asked, f.eventLog())
	}

	// A plan with one QA phase at the end flags none; one with none is told where its full checks go.
	end := strings.Replace(qaDoc, "## P3 — Check them\nAfter: P0, P1, P2\nType: qa\n", "## P3 — Tidy up\nAfter: P0, P1, P2\n", 1)
	if d, why, _ := f.exitPlan("/plans/end.md", end); d != "deny" || strings.Contains(why, "before the last QA phase") || !strings.Contains(why, "- P7 (Check it all): 7 phases since the plan's start\n") {
		t.Errorf("one QA phase at the end: %q\n%s", d, why)
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/end.md", "gates": true, "qa_phases": 1.0, "extra_qa": 0.0}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if d, why, _ := f.exitPlan("/plans/none.md", approvedDoc); d != "deny" || !strings.Contains(why, "This plan has no QA phase") {
		t.Errorf("no QA phase: %q\n%s", d, why)
	}
}
