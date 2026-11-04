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

// TierRestarting tells the human that baton restarts the lead's session in place on tier t, phase's ("" for
// the human's own, as no plan runs): the same conversation, on the flags the tier needs.
func TierRestarting(phase string, t plan.Tier) string {
	if phase == "" {
		return fmt.Sprintf("baton: restarting your session back on your own %s, as no plan runs: the same conversation, on your tier again", t)
	}
	return fmt.Sprintf("baton: restarting your session on %s, %s's tier: the same conversation, on the flags that tier needs (--model, --effort)", t, phase)
}

// TierRestarted is the note a session restarted in place on tier t starts with, for phase ("" for the
// human's own tier).
func TierRestarted(phase string, t plan.Tier) string {
	if phase == "" {
		return fmt.Sprintf("baton: restarted this session back on your own %s · ", t)
	}
	return fmt.Sprintf("baton: restarted this session on %s, %s's tier · ", t, phase)
}

// TierRestartedMail tells the lead's model, restarted in place mid-phase on tier t, to take phase up again:
// nothing of its work is lost, and the conversation is the same.
func TierRestartedMail(phase string, t plan.Tier) string {
	return fmt.Sprintf("baton restarted this session on %s, %s's tier (the same conversation; nothing of your work is lost). "+
		"Take %s up again where you left it.", t, phase, phase)
}
