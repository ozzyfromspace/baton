package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The record of what a run decided without the human (docs/escalation.md). The human finds it in
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

// Happened says what a decision made without the human did: a note, or a proposal baton went ahead with.
func Happened(d state.Decision) string {
	if d.Kind == state.Proposal {
		return fmt.Sprintf("went ahead, as nobody answered by %s: %s", d.Deadline.Local().Format("15:04"), clause(d.What))
	}
	return "noted: " + clause(d.What)
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
