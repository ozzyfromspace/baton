package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The words about a phase's tier, the model and effort it runs on (plan/tier.go, state/tier.go).

// OnTier is " on opus/high", for a line that says what a phase runs on, or "" for a tier left to the
// session.
func OnTier(t plan.Tier) string {
	if t.Zero() {
		return ""
	}
	return " on " + t.String()
}

// TierCapped tells the human that phase asked for tier and its loop was served another: Claude Code clamps
// an effort to the settings' maxEffortLevel, the organization's cap and the model's own, and may serve
// another model, and nothing baton does changes that, so it is said once and the phase goes on.
func TierCapped(phase string, asked, served plan.Tier) string {
	return fmt.Sprintf("baton: %s asked for %s and was served %s. Claude Code chose that (a maxEffortLevel in your settings, "+
		"your organization's cap, or the model's own); the phase goes on as served.", phase, asked, served)
}
