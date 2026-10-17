package decide

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// What baton says about a plan's shape (plan.Shape) when the model puts the plan to the human: one line
// for the human, and, once per plan, for one that is mostly a chain (plan.Shape.MostlyChain), a request
// to the model to check that chain against the shape rules.

// ShapeRules are baton's rules for a plan's shape: when one phase waits for another, and how to keep the
// chains of phases that wait one for another short, so phase agents can work beside the lead. The skill's
// planning guidance (internal/skill/skill.md) says the same to the model making a plan.
const ShapeRules = "1. A dependency is a need. A phase is `After:` another only if it cannot start without something that phase makes: " +
	"code, a schema, a file, a decision. Being checked by another phase's tool or gate is not a need: the QA phase checks both.\n" +
	"2. Split enabling work. The small part others need (an interface, a schema, a local tool, a fixture) goes first, in a phase of its own; " +
	"the rest of that work becomes a leaf.\n" +
	"3. Contract first. Where two pieces meet at an interface, fix the interface in a small phase, then build both sides at once.\n" +
	"4. Split by files. Where phases would edit the same files, divide the work along file ownership rather than chaining it; " +
	"chain only what truly shares files.\n" +
	"5. Outside waits off the main chain. A phase on the main chain checks itself locally. Waiting on CI, a deploy or an outside service " +
	"(`baton await`) belongs in a leaf, a QA phase or the release.\n" +
	"6. A small task can be one phase."

// shapeLineChain is the most phases of a chain PlanShape lists in full; a longer one shows its first and
// last few.
const shapeLineChain = 9

// PlanShape is a plan's shape in one line, for the human when the plan is put to them for approval:
// "baton: 12 phases · longest chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once". A plan of one phase
// has no shape to show: "".
func PlanShape(s plan.Shape) string {
	if s.Phases < 2 {
		return ""
	}
	chain := fmt.Sprintf("longest chain %d (%s)", len(s.Chain), arrows(s.Chain))
	if len(s.Chain) < 2 {
		chain = "no phase waits for another"
	}
	at := fmt.Sprintf("up to %d at once", s.Width)
	if s.Width < 2 {
		at = "one at a time"
	}
	return fmt.Sprintf("baton: %d phases · %s · %s", s.Phases, chain, at)
}

// arrows joins a chain's ids with arrows, "P1 → P7 → P8". One longer than shapeLineChain shows its first
// four and last three around an ellipsis.
func arrows(ids []string) string {
	if len(ids) > shapeLineChain {
		ids = slices.Concat(ids[:4], []string{"…"}, ids[len(ids)-3:])
	}
	return strings.Join(ids, " → ")
}

// ShapeCheck is the part of baton's one turn-back at a plan's first ExitPlanMode, beside
// CompetingDirectives, for a plan that is mostly a chain: its numbers, the whole chain, the shape rules to
// check each link of it against, and that the model may put the plan to the human with the chain as it
// is, saying in the plan why it is real.
func ShapeCheck(s plan.Shape) string {
	at := fmt.Sprintf("At most %d phases can be worked on at once", s.Width)
	if s.Width < 2 {
		at = "Only one phase can be worked on at a time"
	}
	return fmt.Sprintf("[baton] Check this plan's shape too. Of its %d phases, %d form one chain, each waiting for the one before it: %s. "+
		"%s, so baton's phase agents, which work beside you only on phases whose dependencies are done, would mostly wait, "+
		"and the run would take about as long as running its phases one by one. Check each link of that chain against baton's shape rules:\n\n%s\n\n"+
		"Where a link does not hold, change the plan (its `After:` lines, or split a phase as the rules say) so more of it can run at once. "+
		"Where every link holds, the chain is real: you may call ExitPlanMode again with the phases as they are, "+
		"adding to the plan (in its Standing rules) a line on why the chain is real. baton asks this once per plan too.",
		s.Phases, len(s.Chain), strings.Join(s.Chain, " → "), at, ShapeRules)
}

// PlanChecked tells the human what Claude does before they see the plan: it checks the plan against their
// other instructions (CompetingDirectives), its gates against baton's gate rules (GateCheck), and, for a
// plan that is mostly a chain (flagged), its longest chain against baton's shape rules (ShapeCheck).
func PlanChecked(s plan.Shape, flagged bool) string {
	line := "baton: before you see the plan, Claude checks it against your other instructions (CLAUDE.md, memory) for anything that competes with it"
	if !flagged {
		return line + ", and checks its gates and QA phases against baton's gate rules"
	}
	return line + fmt.Sprintf(", its gates and QA phases against baton's gate rules, and its longest chain (%d of %d phases) against baton's shape rules", len(s.Chain), s.Phases)
}
