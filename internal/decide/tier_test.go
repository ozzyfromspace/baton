package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The tier rules say what each tier is for and what it costs, err on the side of caution, name Fable only
// where the human allows it, and say how baton raises a phase, as configured.
func TestTierRules(t *testing.T) {
	r := TierRules(false, 2, 3)
	for _, want := range []string{"Err on the side of caution", "**opus/high** is the default", "max 3.44", "after 3 red runs", "at most 2 steps", "never lowers one mid-phase"} {
		if !strings.Contains(r, want) {
			t.Errorf("the rules lack %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "fable") {
		t.Errorf("Fable named where the human does not allow it:\n%s", r)
	}
	if r := TierRules(true, 0, 3); !strings.Contains(r, "(haiku, sonnet, opus or fable)") || !strings.Contains(r, "bill beyond a subscription's plan") || !strings.Contains(r, "No raises.") {
		t.Errorf("with Fable allowed and no raises:\n%s", r)
	}
}

// The plan's tiers in the approval line, the most common first.
func TestPlanTiers(t *testing.T) {
	pl := plan.Plan{Phases: []plan.Phase{{ID: "P0", Model: "opus", Effort: "high"}, {ID: "P1"}, {ID: "P2", Model: "opus", Effort: "high"}, {ID: "P3", Model: "sonnet", Effort: "low"}}}
	if got := PlanTiers(pl); got != "tiers 2 opus/high, 1 the session's, 1 sonnet/low" {
		t.Errorf("PlanTiers: %q", got)
	}
	if got := PlanTiers(plan.Plan{Phases: []plan.Phase{{ID: "P0"}}}); got != "" {
		t.Errorf("a plan with no tier: %q", got)
	}
	if got := RaiseWhy(3, "go test ./..."); got != "3 red runs of its gate (`go test ./...`)" {
		t.Errorf("RaiseWhy: %q", got)
	}
	if got := TierRaised("P3", plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "xhigh"}, "3 red runs of its gate", false); !strings.Contains(got, "raised P3 from opus/high to opus/xhigh after 3 red runs of its gate; a fresh phase agent") {
		t.Errorf("TierRaised: %q", got)
	}
}
