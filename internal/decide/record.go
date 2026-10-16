package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The record of what a run decided without the human (docs/design.md). The human finds it in
// baton status, at each phase's end and at the plan's; the model is handed it after every compaction and
// whenever the human writes, so that a late "undo that" finds its undo even in a context that never saw
// the decision (the S3 spike, docs/research/escalation.md).

// MostListed is how many decisions a list for the model shows; baton status has the rest.
const MostListed = 10

// WithoutHuman lists the decisions in ds the run made without the human, oldest first.
func WithoutHuman(ds []state.Decision) []state.Decision {
	var out []state.Decision
	for _, d := range ds {
		if d.WithoutHuman() {
			out = append(out, d)
		}
	}
	return out
}

// Tally counts decisions made without the human, for the human: "3 decisions made without you".
func Tally(n int) string {
	if n == 1 {
		return "1 decision made without you"
	}
	return fmt.Sprintf("%d decisions made without you", n)
}

// LeftForYou lists the proposals in ds answered Not now (state.Decision.LeftForHuman), oldest first: each
// was done only if it was safely reversible and stayed inside the project, and noted then; otherwise it is
// left for the human.
func LeftForYou(ds []state.Decision) []state.Decision {
	var out []state.Decision
	for _, d := range ds {
		if d.LeftForHuman() {
			out = append(out, d)
		}
	}
	return out
}

// PlanDone says a plan is complete: how many phases it had, what phase agents saved it (Saved), how many
// decisions the run (its workers too) made without the human, and how many of its proposals were
// answered Not now (left for them), which baton status lists.
func PlanDone(phases, decided, left int, saving state.Saving) string {
	s := fmt.Sprintf("all %d phases done", phases)
	if phases == 1 {
		s = "its 1 phase done"
	}
	if saved := Saved(saving); saved != "" {
		s += " · " + saved
	}
	if decided > 0 {
		s += " · " + Tally(decided)
	}
	if left > 0 {
		s += fmt.Sprintf(" · %d %s left for you", left, plural(left, "proposal", "proposals"))
	}
	if decided > 0 || left > 0 {
		s += " — /baton status"
	}
	return s
}

// Saved says what phase agents saved a complete plan (state.Saving), for the human: "phase agents saved
// about 3h20m: 8h29m of phase work in 5h09m". Each figure is to the minute (to the second under one), and
// what was saved is the difference of the two shown, so that a line in minutes adds up. "" for a plan no
// phase agent worked on.
func Saved(s state.Saving) string {
	if s.Agents == 0 {
		return ""
	}
	work, wall := roundSpan(s.Work), roundSpan(s.Wall)
	if saved := work - wall; saved > 0 {
		return fmt.Sprintf("phase agents saved about %s: %s of phase work in %s", Span(saved), Span(work), Span(wall))
	}
	return fmt.Sprintf("phase agents saved no time: %s of phase work in %s", Span(work), Span(wall))
}

// roundSpan is d rounded for Span, which cuts what it does not show: to the minute, or under one to the second.
func roundSpan(d time.Duration) time.Duration {
	if d = d.Round(time.Second); d >= time.Minute {
		return d.Round(time.Minute)
	}
	return d
}

// Happened says what a decision made without the human did: a note, or a proposal baton went ahead with,
// at its deadline or once its hold for the human lapsed.
func Happened(d state.Decision) string {
	if d.Kind == state.Proposal && !d.Lapsed.IsZero() {
		return fmt.Sprintf("went ahead (or a more reversible way did) once its hold for you lapsed at %s, as nobody answered: %s", d.Lapsed.Local().Format("15:04"), clause(d.What))
	}
	if d.Kind == state.Proposal {
		return fmt.Sprintf("went ahead, as nobody answered by %s: %s", d.Deadline.Local().Format("15:04"), clause(d.What))
	}
	return "noted: " + clause(d.What)
}

// LeftHow says what became of a proposal answered Not now (state.Decision.LeftForHuman), for the human.
func LeftHow(d state.Decision) string {
	who := "nobody answered by " + d.Deadline.Local().Format("15:04") + ", so baton answered Not now"
	switch {
	case d.Resolved.By == state.ByHuman:
		who = "you answered Not now"
	case !d.Lapsed.IsZero():
		who = "its hold for you lapsed at " + d.Lapsed.Local().Format("15:04") + ", so baton answered Not now"
	}
	return fmt.Sprintf("%s: done only if it was safely reversible and stayed in the project (a note then says so), otherwise left for you — %s", who, clause(d.What))
}

// When is when a decision took effect: a note when it was made, a proposal when baton went ahead.
func When(d state.Decision) time.Time {
	if r := d.Resolved; r != nil {
		return r.At
	}
	return d.At
}

// Stamp is a time as the record shows it.
func Stamp(t time.Time) string { return t.Local().Format("Jan 2 15:04") }

// UndoOf is how to reverse a decision, as the model gave it.
func UndoOf(d state.Decision) string {
	if d.Undo == "" {
		return "none was given"
	}
	return d.Undo
}

// Entry is one decision made without the human, on a line for the model.
func Entry(d state.Decision) string {
	return fmt.Sprintf("%s (%s, %s) %s. Undo: %s", d.ID, d.Phase, Stamp(When(d)), Happened(d), UndoOf(d))
}

// Listed lists ds for the model, a "- " line each: the newest most of them, after a line counting the
// older ones left out.
func Listed(ds []state.Decision, most int) string {
	var b strings.Builder
	if older := len(ds) - most; most > 0 && older > 0 {
		fmt.Fprintf(&b, "- …and %d earlier (baton status lists every one)\n", older)
		ds = ds[older:]
	}
	for _, d := range ds {
		b.WriteString("- " + Entry(d) + "\n")
	}
	return b.String()
}

// RecordSection is the brief's account of the decisions made without the human so far.
func RecordSection(ds []state.Decision) string {
	return "Decisions made without the human so far (if they ask, each has its undo):\n\n" + Listed(ds, MostListed)
}

// LateAnswer hands the model, as the human writes to it, the decisions made without them since they last
// did: what they wrote may be about one of them, and its undo is then at hand.
func LateAnswer(ds []state.Decision) string {
	return "Decisions this run made without the human since they last wrote to you, oldest first. What they just wrote may be about one of them: " +
		"undo one only if they ask, with the undo given here.\n" + Listed(ds, MostListed)
}
