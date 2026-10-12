package decide

import (
	"fmt"
	"strings"
)

// Stop is a session baton cannot carry on by itself, as the escalation that says so describes it.
type Stop struct {
	// Phase is the phase it held ("" for a lead that held none).
	Phase string
	// Worker says it is the session of the worker on Phase, which the human answers in that session
	// (baton view), not the lead's.
	Worker bool
	// Workers says the run has workers beside the lead: while the lead is stopped, the phases that do not
	// need its phase go on. (While a worker is stopped, the rest of the run always goes on.)
	Workers bool
	// Dependents is the phases left that need Phase, directly or not: they wait for it.
	Dependents []string
}

// CannotGoOn is the escalation for a session whose turn ended in a way baton cannot retry away (why: an
// API error that will not pass on its own). It names what stopped, says that only the human can decide
// how it goes on, and what the rest of the run does meanwhile (Stop.meanwhile).
func CannotGoOn(s Stop, why string) string {
	var b strings.Builder
	switch {
	case s.Worker:
		fmt.Fprintf(&b, "%s stopped: %s. Only you can decide how %s goes on, in the worker's session (baton view %s)", s.Phase, why, s.Phase, s.Phase)
	case s.Phase == "":
		fmt.Fprintf(&b, "the lead session stopped: %s. Only you can decide how it goes on", why)
	default:
		fmt.Fprintf(&b, "%s stopped: %s. Only you can decide how %s goes on", s.Phase, why, s.Phase)
	}
	b.WriteString("; " + s.meanwhile())
	return b.String()
}

// Refused is the escalation for a session whose turn the model's safeguards declined, which Claude Code
// would not go on with (category: the API's, "" if it named none). baton never retries it, and starts no
// fresh conversation by itself: the human decides how the phase goes on, in one of the two ways Claude
// Code's own guidance gives (docs/research/recovery.md). It then says what the rest of the run does
// meanwhile.
func Refused(s Stop, category string) string {
	if category != "" {
		category = " (" + category + ")"
	}
	var b strings.Builder
	switch {
	case s.Worker:
		fmt.Fprintf(&b, "%s stopped: the model's safeguards declined the last turn of its worker%s, and that conversation will not go on as it is. "+
			"baton does not retry it. To go on, in the worker's session (baton view %s), ", s.Phase, category, s.Phase)
	case s.Phase == "":
		fmt.Fprintf(&b, "the lead session stopped: the model's safeguards declined the last turn%s, and this conversation will not go on as it is. "+
			"baton does not retry it. To go on, ", category)
	default:
		fmt.Fprintf(&b, "%s stopped: the model's safeguards declined the last turn%s, and this conversation will not go on as it is. "+
			"baton does not retry it. To go on, ", s.Phase, category)
	}
	b.WriteString("rewind (Esc Esc) to before the flagged turn and take a different approach, as Claude Code advises, or /clear for a fresh conversation")
	if s.Phase != "" {
		fmt.Fprintf(&b, " from %s's brief", s.Phase)
	}
	m := s.meanwhile()
	b.WriteString(". " + strings.ToUpper(m[:1]) + m[1:])
	return b.String()
}

// meanwhile is what the rest of the run does while the session is stopped, as a clause: for a worker,
// the rest of the run carries on; for a lead with workers, they carry on with every phase that does not
// need its phase, and the phases that need it wait for it; for a lead without, nothing runs.
func (s Stop) meanwhile() string {
	if !s.Worker && !s.Workers {
		return "nothing else runs until then."
	}
	who, rest := "workers carry on", " with the rest of the plan"
	if s.Worker {
		who, rest = "the rest of the run carries on", ""
	}
	switch len(s.Dependents) {
	case 0:
		return "meanwhile " + who + rest + "."
	case 1:
		return fmt.Sprintf("meanwhile %s with every phase that does not need %s, and %s waits for it.", who, s.Phase, s.Dependents[0])
	}
	return fmt.Sprintf("meanwhile %s with every phase that does not need %s. %s wait for it.", who, s.Phase, andList(s.Dependents))
}

// andList joins items as "a", "a and b", "a, b and c".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
