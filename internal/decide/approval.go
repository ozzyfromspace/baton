package decide

import (
	"fmt"
	"time"
)

// What baton says when the human approves a plan in plan mode (docs/research/sessions.md). baton attaches
// a plan with phase headings itself; the model is only told what happened and what to do next.

// Answers when the human approves a plan while a run has one. Claude Code writes every plan a session
// makes to the same file, so a plan in the run's own file is a revision of it; one in another file
// (planned after a /clear) is a new plan, and the current run's document is still there to keep. Wait for
// me comes first, where the selection starts, so a stray Enter (a second press of the one that approved
// the plan) holds the approved plan for the human, which lapses into the default like any hold
// (HoldLapse), and never sets the run's progress aside: Start the new plan from P0, which does, takes a
// choice. The default (PlanQuestionDefault) is third, as in every question of baton's that has one
// (EscalationOptions).
var (
	ReviseOptions = []Option{
		{"Wait for me", "Change nothing yet; I'll write in the session, or the revised plan goes on after " + Spell(HoldLapse) + " with no word"},
		{"Start the new plan from P0", "Drop this run's progress and run the approved plan from the beginning"},
		{"Continue with the revised plan", "Phases already done stay done; the run goes on from the first one left"},
	}
	ReplaceOptions = []Option{
		{"Wait for me", "Change nothing yet; I'll write in the session, or the current run goes on after " + Spell(HoldLapse) + " with no word"},
		{"Start the new plan from P0", "Stop the current run and run the plan you just approved"},
		{"Keep the current run", "Leave the approved plan aside; the current run goes on"},
	}
)

// PlanQuestion asks the human what to do with a plan they approved while the run of title was at
// phase (or paused there). same says the approved plan is a revision of the run's own.
func PlanQuestion(title, phase, mode string, same bool) Question {
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
	return fmt.Sprintf("[baton] baton attached the plan the human just approved: %q, %s (%s). Do not start %s in this turn: end your turn now. "+
		"baton compacts the planning conversation away and starts %s with a fresh brief: the phase's instructions, the standing rules and your notes.",
		title, PhaseCount(phases), file, first, first)
}

// AttachedNow tells the model baton attached the plan it was just handed after a "clear context"
// approval: the context is fresh already, so the first phase starts at once.
func (w Words) AttachedNow(title string, phases int, file, first, firstTitle string) string {
	return fmt.Sprintf("[baton] baton attached the plan you were just given: %q, %s (%s), and runs it phase by phase, compacting the context between phases. "+
		"Work on %s (%s) only. %s", title, PhaseCount(phases), file, first, firstTitle, w.Reporting())
}

// PhaseCount says how many phases a plan has: "1 phase", "12 phases".
func PhaseCount(n int) string {
	return fmt.Sprintf("%d %s", n, plural(n, "phase", "phases"))
}

// PlanQuestionDefault is what baton answers, when nobody has by its deadline, about a plan the human approved
// mid-run: a revision of the run's own plan is followed (done phases stay done, nothing is lost), and
// another plan is left aside for /baton run, since starting it would set the current run's progress aside.
func PlanQuestionDefault(same bool) string {
	if same {
		return ReviseOptions[2].Label
	}
	return ReplaceOptions[2].Label
}

// Chose says who chose an answer: the human, or baton for them since nobody answered by deadline
// (auto).
func Chose(auto bool, deadline time.Time) string {
	if auto {
		return "Nobody answered by " + deadline.Local().Format("15:04") + ", so baton chose"
	}
	return "The human chose"
}

// PlanQuestionWaits tells the human that the model stopped without asking about the plan they approved, so
// baton answers answer for them at deadline unless they do first.
func PlanQuestionWaits(answer string, deadline time.Time) string {
	return fmt.Sprintf("baton: Claude did not ask you about the plan you approved → baton answers %q at %s unless you do first (/baton run <file> attaches a plan any time)",
		answer, deadline.Local().Format("15:04"))
}

// PlanQuestionHolding is what baton says to the human (say) and tells the model (tell) as it holds the
// plan they approved mid-run (file) at their word (Wait for me): nothing changes until they write, and
// with no word from them in lapse, baton answers answer (PlanQuestionDefault) for them.
func PlanQuestionHolding(file, answer string, lapse time.Duration) (say, tell string) {
	say = fmt.Sprintf("baton: holding the plan you approved for you — write in the session; with no word from you in %s, baton answers %q", Spell(lapse), answer)
	tell = fmt.Sprintf("[baton] The human chose Wait for me about the plan they approved (%s): change nothing, and end your turn now. "+
		"The run waits for them, and their answer will reach you here; with no word from them in %s, baton answers %q for them and tells you.", file, Spell(lapse), answer)
	return say, tell
}

// PlanQuestionHeldWaits tells the human, when the model stops, that the plan they approved waits for their
// word (Wait for me) until deadline, when baton answers answer.
func PlanQuestionHeldWaits(answer string, deadline time.Time) string {
	return fmt.Sprintf("baton: waiting on you about the plan you approved → baton answers %q at %s unless you write first", answer, deadline.Local().Format("15:04"))
}

// PlanQuestionHeldTurn is why the model ends its turn while the plan the human approved waits for their word.
const PlanQuestionHeldTurn = "the human chose Wait for me about the plan they approved, and the run waits for them"

// PlanQuestionUnsettled tells the model that the human wrote while baton's question about the plan they
// approved (file) was unsettled, unanswered or held for them (held): baton changed nothing, and their
// message is what to do.
func PlanQuestionUnsettled(file string, held bool) string {
	what := "baton's question about the plan the human approved (" + file + ") went unanswered"
	if held {
		what = "the human chose Wait for me on baton's question about the plan they approved (" + file + "), and has now written"
	}
	return "[baton] " + what + ", so baton changed nothing. Their message is what to do; if they want that plan run, `/baton run` attaches it."
}
