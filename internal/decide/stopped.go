package decide

import (
	"fmt"
	"strings"
)

// CannotGoOn is the escalation for a lead whose turn ended in a way baton cannot retry away (why: an API
// error that will not pass on its own). It names what stopped, says that only the human can decide how
// it goes on, and what the rest of the run does meanwhile: with workers, every phase that does not need
// phase goes on, and waiting (the phases that need it, directly or not) wait for it; without, nothing
// runs. phase is "" when the lead held none.
func CannotGoOn(phase, why string, waiting []string, workers bool) string {
	var b strings.Builder
	if phase == "" {
		fmt.Fprintf(&b, "the lead session stopped: %s. Only you can decide how it goes on", why)
	} else {
		fmt.Fprintf(&b, "%s stopped: %s. Only you can decide how %s goes on", phase, why, phase)
	}
	switch {
	case !workers:
		b.WriteString("; nothing else runs until then.")
	case phase == "" || len(waiting) == 0:
		b.WriteString("; meanwhile workers carry on with the rest of the plan.")
	case len(waiting) == 1:
		fmt.Fprintf(&b, "; meanwhile workers carry on with every phase that does not need %s, and %s waits for it.", phase, waiting[0])
	default:
		fmt.Fprintf(&b, "; meanwhile workers carry on with every phase that does not need %s. %s wait for it.", phase, andList(waiting))
	}
	return b.String()
}

// andList joins items as "a", "a and b", "a, b and c".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
