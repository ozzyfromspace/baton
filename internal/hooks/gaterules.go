package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
)

// gateCheck is the gate rules' part of baton's one turn-back at a plan's first ExitPlanMode
// (decide.GateCheck), asked of every plan beside the competing directives, and adds to fields, which
// directives_asked logs, `gates: true` and, for a plan that builds, how many QA phases it has
// (`qa_phases`) and how many of them come before its last (`extra_qa`, decide.ExtraQA). A plan that does
// not build gets the rules alone.
func gateCheck(pl plan.Plan, built bool, fields map[string]any) string {
	fields["gates"] = true
	if !built {
		return decide.GateCheck(plan.Plan{})
	}
	fields["qa_phases"], fields["extra_qa"] = pl.Shape().QA, len(decide.ExtraQA(pl))
	return decide.GateCheck(pl)
}
