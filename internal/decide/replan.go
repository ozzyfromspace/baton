package decide

import (
	"fmt"
	"strings"
)

// PlanChanged is baton's message to the agent at work on phase once the human changed the plan: what
// changed for it, as the plan reads now. rules is the standing rules and section its phase's section,
// each "" when it reads as the agent was given it.
func PlanChanged(phase, rules, section string) string {
	b := fmt.Sprintf("[baton] The human changed the plan while you work on %s, and baton follows the new text. Below is %s as the plan reads now: "+
		"follow it from here on. The commits you made stay as they are: where one no longer fits, change it with a new commit, and say so in your notes.",
		phase, planParts(phase, rules != "", section != ""))
	for _, text := range []string{rules, section} {
		if text != "" {
			b += "\n\n" + strings.TrimSpace(text)
		}
	}
	return b
}

// PlanTold tells the human that the agent on phase was told the new text of the plan: the standing rules
// (rules), its phase's section (section), or both.
func PlanTold(phase string, rules, section bool) string {
	return fmt.Sprintf("baton: the plan changed → the agent on %s is told %s as it reads now", phase, planParts(phase, rules, section))
}

// planParts names the parts of the plan that changed for the agent on phase.
func planParts(phase string, rules, section bool) string {
	switch {
	case rules && section:
		return "the standing rules and " + phase + "'s section"
	case rules:
		return "the standing rules"
	}
	return phase + "'s section"
}
