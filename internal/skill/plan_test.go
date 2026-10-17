package skill

import (
	"strings"
	"testing"
)

// planSection is the text of the skill's `plan <goal>` section, its line ends as written (a checkout on
// Windows may turn them into CRLF).
func planSection(t *testing.T) string {
	t.Helper()
	text := strings.ReplaceAll(Text, "\r\n", "\n")
	start := strings.Index(text, "\n## plan <goal>\n")
	if start < 0 {
		t.Fatal("the skill has no plan section")
	}
	rest := text[start+1:]
	if end := strings.Index(rest[1:], "\n## "); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}

// /baton plan teaches a graph, not a list: the six shape rules, the old same-files chaining only as the last
// resort of splitting by files, the shape baton shows at approval, and the rest of the section as it was.
func TestPlanTeachesTheShapeRules(t *testing.T) {
	plan := planSection(t)
	for _, c := range []struct {
		name, want string
	}{
		{"rule 1", "1. **A dependency is a need.**"},
		{"rule 1, a gate is no need", "Being checked by another phase's tool or gate is not a need"},
		{"rule 2", "2. **Split enabling work.**"},
		{"rule 3", "3. **Contract first.**"},
		{"rule 4", "4. **Split by files.**"},
		{"rule 4, chaining the last resort", "Only as a last resort, for work that truly shares files, do phases go one after the other"},
		{"rule 5", "5. **Outside waits off the main chain.**"},
		{"rule 5, await off the chain", "(`baton await`) belongs in a leaf, a QA phase or the release"},
		{"rule 6", "6. **A small task can be one phase.**"},
		{"one or more phases", "a formal plan of one or more phases"},
		{"the longest chain sets the length", "its longest chain of `After:` lines"},
		{"the shape shown at approval", "baton shows its shape in one line"},
		{"asked once about a long chain", "baton asks you once, at your first `ExitPlanMode`"},
		{"may resubmit unchanged", "say why in the plan and call `ExitPlanMode` again unchanged"},
		{"After lines", "`After: P1, P3`, `After: none`, or `After: all`"},
		{"After: all", "release phases use `After: all`"},
		{"QA phases", "`Type: qa`"},
		{"QA phases at real seams", "Place QA phases at real seams, never per phase, to protect the human's time: at the end, " +
			"and in a long plan at the end of a group of related phases that later work builds on, each `After:` everything it checks."},
		{"scoped checks between them", "The phases between QA phases run scoped checks only (the tests of what they change, by name)"},
		{"the gate rules asked at approval", "baton asks for this, with its gate rules, once at your first `ExitPlanMode`"},
		{"sizing", "Size each phase to finish comfortably in one context window."},
		{"standing rules", "`## Standing rules`"},
		{"verification", "`## Verification`"},
		{"competing directives", "address in the plan anything that competes with it"},
	} {
		if !strings.Contains(plan, c.want) {
			t.Errorf("%s: the plan section lacks %q", c.name, c.want)
		}
	}
	for _, old := range []string{
		"Mark a phase independent of another only when it needs nothing that phase produces",
		"Phases that would edit the same files go one after the other",
		"multi-phase plan",
		"one after several phases, and one before a release",
		"focused checks only",
	} {
		if strings.Contains(plan, old) {
			t.Errorf("the plan section still says %q", old)
		}
	}
}

// /baton plan teaches the Replan: after line, for a spike or an exploration whose findings should reshape
// the phases after it.
func TestPlanTeachesReplanAfter(t *testing.T) {
	plan := planSection(t)
	for _, want := range []string{
		"Give a spike or an exploration",
		"a line `Replan: after` under its heading",
		"never dropping work",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan section lacks %q", want)
		}
	}
}
