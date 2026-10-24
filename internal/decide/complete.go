package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// PlanCompleteMail is baton's message to the lead when phase's landing completed a plan of phases: the
// lead held no phase, so no stop or done of its own reports the end, and it is woken to sum the run up as
// it would at its last done. saving is what phase agents saved it (Saved), and ds the run's decisions, its
// phase agents' too (PlanComplete).
func PlanCompleteMail(phase string, phases int, saving state.Saving, ds []state.Decision) string {
	done := fmt.Sprintf("all %d phases done", phases)
	if phases == 1 {
		done = "its 1 phase done"
	}
	if saved := Saved(saving); saved != "" {
		done += " (" + saved + ")"
	}
	return fmt.Sprintf("[baton] %s's work landed on the run's branch, and it was the last thing the plan waited for: the plan is complete, %s. ", phase, done) +
		PlanComplete(ds)
}

// MailDone is the line baton types to wake an idle lead for PlanCompleteMail (MailTyped).
const MailDone = "[baton] The plan is complete: baton's message, attached, says what to sum up for the human."
