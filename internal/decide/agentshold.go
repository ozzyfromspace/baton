package decide

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// AgentsHold is the lead's stop line with no phase in hand when phase agents' work alone holds the run
// (state.HeldByAgents), or "" when h holds nothing: what the run waits on, and that it waits on nothing of
// the human's. Every phase left with an agent: "baton: P4, the last phase, is with its phase agent — baton
// says here when it lands and the plan is complete. Nothing waits on you." Phases waiting on agents' work:
// "baton: no phase can start until P3's agent lands its work (P5 waits on it) — baton takes the next one
// up then. Nothing waits on you."
func AgentsHold(h state.AgentsHold) string {
	const free = " Nothing waits on you."
	switch {
	case h.Last && len(h.AtWork) == 1:
		return fmt.Sprintf("baton: %s, the last phase, is with its phase agent — baton says here when it lands and the plan is complete.", h.AtWork[0]) + free
	case h.Last:
		return fmt.Sprintf("baton: %s, the last phases, are with phase agents — baton says here when they land and the plan is complete.", andList(h.AtWork)) + free
	case len(h.Behind) == 0:
		return ""
	}
	return "baton: no phase can start until " + awaited(h.Behind) + " — baton takes the next one up then." + free
}

// awaited says what the phases of behind wait on, after "until": the work of one agent, or of the same
// agents, with the phases behind it ("P3's agent lands its work (P5 waits on it)", "P3's and P4's agents
// land their work (P5 and P6 wait on them)"), or each phase's own ("phase agents land their work (P5 waits
// on P3's agent, P6 on P3's and P4's agents)").
func awaited(behind []state.Behind) string {
	same := !slices.ContainsFunc(behind, func(b state.Behind) bool { return !slices.Equal(b.On, behind[0].On) })
	if same {
		var phases []string
		for _, b := range behind {
			phases = append(phases, b.Phase)
		}
		wait := "waits"
		if len(phases) > 1 {
			wait = "wait"
		}
		if on := behind[0].On; len(on) > 1 {
			return fmt.Sprintf("%s agents land their work (%s %s on them)", andList(possessive(on)), andList(phases), wait)
		}
		return fmt.Sprintf("%s's agent lands its work (%s %s on it)", behind[0].On[0], andList(phases), wait)
	}
	var each []string
	for i, b := range behind {
		on, agents := " on ", " agent"
		if i == 0 {
			on = " waits on "
		}
		if len(b.On) > 1 {
			agents = " agents"
		}
		each = append(each, b.Phase+on+andList(possessive(b.On))+agents)
	}
	return "phase agents land their work (" + strings.Join(each, ", ") + ")"
}

// possessive is each phase of phases with 's: "P3's".
func possessive(phases []string) []string {
	out := make([]string, len(phases))
	for i, p := range phases {
		out[i] = p + "'s"
	}
	return out
}
