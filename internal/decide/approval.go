package decide

import "fmt"

// What baton says when the human approves a plan in plan mode (docs/research/sessions.md). baton attaches
// a plan with phase headings itself; the model is only told what happened and what to do next.

// Answers when the human approves a plan while a run has one. Claude Code writes every plan a session
// makes to the same file, so a plan in the run's own file is a revision of it; one in another file
// (planned after a /clear) is a new plan, and the current run's document is still there to keep.
var (
	ReviseOptions = []Option{
		{"Continue with the revised plan", "Phases already done stay done; the run goes on from the first one left"},
		{"Start the new plan from P0", "Drop this run's progress and run the approved plan from the beginning"},
	}
	ReplaceOptions = []Option{
		{"Start the new plan from P0", "Stop the current run and run the plan you just approved"},
		{"Keep the current run", "Leave the approved plan aside; the current run goes on"},
	}
)

// ReplanQuestion asks the human what to do with a plan they approved while the run of title was at
// phase (or paused there). same says the approved plan is a revision of the run's own.
func ReplanQuestion(title, phase, mode string, same bool) Question {
	where := "is running"
	if mode == "paused" {
		where = "is paused"
	}
	if phase != "" {
		where += " (at " + phase + ")"
	}
	if same {
		return Question{
			Text:    fmt.Sprintf("baton: you approved a revised plan while %s %s. Carry on with the revision, or start it over from P0?", Sanitize(fmt.Sprintf("%q", title)), where),
			Options: ReviseOptions,
		}
	}
	return Question{
		Text:    fmt.Sprintf("baton: you approved a new plan while %s %s. Start the new plan from P0, or keep the current run?", Sanitize(fmt.Sprintf("%q", title)), where),
		Options: ReplaceOptions,
	}
}

// AttachedAtBoundary tells the model baton attached the plan just approved and will compact before its
// first phase, so the turn must end now.
func AttachedAtBoundary(title string, phases int, file, first string) string {
	return fmt.Sprintf("[baton] baton attached the plan the human just approved: %q, %d phases (%s). Do not start %s in this turn: end your turn now. "+
		"baton compacts the planning conversation away and starts %s with a fresh brief: the phase's instructions, the standing rules and your notes.",
		title, phases, file, first, first)
}

// AttachedNow tells the model baton attached the plan it was just handed after a "clear context"
// approval: the context is fresh already, so the first phase starts at once.
func (w Words) AttachedNow(title string, phases int, file, first, firstTitle string) string {
	return fmt.Sprintf("[baton] baton attached the plan you were just given: %q, %d phases (%s), and runs it phase by phase, compacting the context between phases. "+
		"Work on %s (%s) only. %s", title, phases, file, first, firstTitle, w.Reporting())
}
