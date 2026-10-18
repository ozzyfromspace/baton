package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Proportion at queue time (v0.8.7): `baton verify` questions a gate out of proportion to the change once,
// as it is queued, and the same call again queues it (cli/proportion.go). Nothing waits on the human, and
// nothing is refused twice: a question only puts the narrower check in front of the model before a long
// gate takes the human's time.

// GateLong is how long an earlier run of a gate's command must have taken for baton to count it long.
const GateLong = 10 * time.Minute

// GateListed is how many of the files changed since an earlier run a question lists.
const GateListed = 20

// The kinds of question baton asks of a gate as it is queued: each is asked once per command and commit
// (state.Run.GatesQuestioned) and logged as gate_questioned's type.
const (
	GateRepeat   = "repeat"         // its command ran before, long, on the same or nearly the same code
	GateOutside  = "outside_script" // it runs a script that is in no commit
	GateUnstaged = "unstaged"       // it ran long before, with no stage marker in its log
	GateBeforeQA = "long_before_qa" // a long gate in a phase before the plan's QA phase
	GateQAAsk    = "qa_ask"         // QAAsk, put with the questions above rather than after the job is queued
)

// GateQuestioned is the refusal that puts questions to the model about gate command before it runs, once,
// led by lead (GateLead: what it spends of the human's time, and what the phase spent so far) when there is
// one. Queued again with --why, it runs, and the human is told the reason (GateAnswered, RepeatSaid).
func GateQuestioned(command, lead string, questions []string) string {
	var b strings.Builder
	if lead != "" {
		fmt.Fprintf(&b, "not queued, this once. %s\nbaton questions `%s` before it runs:\n", lead, command)
	} else {
		fmt.Fprintf(&b, "not queued, this once — baton questions `%s` before it runs, as a long gate spends the human's time:\n", command)
	}
	for _, q := range questions {
		b.WriteString("- " + q + "\n")
	}
	b.WriteString("Queue the narrower check if one proves the change. " +
		"To run the whole gate anyway, queue it again with `--why \"…\"`: baton tells the human your reason.")
	return b.String()
}

// Repeat is a gate whose command ran before, long (GateRepeat).
type Repeat struct {
	// Prev is the command's latest run with a verdict, which took Took.
	Prev state.Job
	Took time.Duration
	// Same is set when the gate would run on the code Prev ran on: its commit, or one that changes no file.
	// Otherwise Changed lists the files changed since (at most GateListed, More the rest), nil when git
	// could not say.
	Same    bool
	Changed []string
	More    int
	// Failed is set when `baton verify --failed` would rerun what failed in Prev (a red gate built from
	// stages).
	Failed bool
	// Failures are the tests a red Prev's log says failed (lane.FailureIDs), at most GateListed of them, and
	// MoreFailures the rest.
	Failures     []lane.FailedTest
	MoreFailures int
	// Green is the command's latest run that passed, when only tests and documents changed since its commit:
	// the code it checked is the code the gate would check.
	Green *state.Job
}

// Question is what baton asks of a repeat in phase.
func (r Repeat) Question(phase string) string {
	p := r.Prev
	ran := fmt.Sprintf("job %s ran `%s` for %s", p.ID, p.Command, Span(r.Took))
	var s string
	switch {
	case r.Same && p.Status == state.JobPassed:
		s = fmt.Sprintf("A repeat: %s on this same code, and it passed. Running it again proves nothing new: go on with what is left of %s, or report it done.",
			ran, phase)
	case r.Same:
		s = fmt.Sprintf("A repeat: %s on this same code, and it %s. Running it whole again spends that time again to learn what its result already says: %s",
			ran, verdict(p), r.again())
	default:
		s = fmt.Sprintf("A repeat: %s on %s, and it %s. Since then %s. Does a narrower check prove the change: the tests of %s",
			ran, short7(p.Commit), verdict(p), changedSince(r.Changed, r.More), those(r.Changed, r.More))
		if o := r.onlyFailed(); o != "" {
			s += ", or " + o
		}
		s += "?"
	}
	if g := r.Green; g != nil && !(r.Same && p.Status == state.JobPassed) {
		s += fmt.Sprintf(" Code outside the tests is unchanged since job %s passed in full on %s: only tests and documents changed since. "+
			"That run is the evidence to ship on: run the changed tests by name, and report %s done.", g.ID, short7(g.Commit), phase)
		if p.Red() {
			s += fmt.Sprintf(" If what is red is on the tests' side, finish with `baton done %s --despite-red \"<why>\"`.", phase)
		}
	}
	return s
}

// again is how to rerun only what failed in a red gate on the same code.
func (r Repeat) again() string {
	switch {
	case r.Failed:
		return "`baton verify --failed --notes \"…\"` reruns only what failed, carrying the stages that passed."
	case len(r.Failures) > 0:
		return "rerun " + r.onlyFailed() + "."
	}
	return "rerun only what failed, by name (its result lists the failures), and the gate once the fix is in."
}

// verdict is how a job with a verdict ended: "passed", "failed", "ran out of time".
func verdict(j state.Job) string {
	switch j.Status {
	case state.JobPassed:
		return "passed"
	case state.JobTimeout:
		return "ran out of time"
	}
	return "failed"
}

// changedSince lists the files changed since a run: "3 files changed: a.go, b.go, c.go".
func changedSince(changed []string, more int) string {
	n := len(changed) + more
	switch {
	case changed == nil && more == 0:
		return "the code moved on (git could not list the files changed)"
	case n == 1:
		return "1 file changed: " + changed[0]
	}
	s := fmt.Sprintf("%d files changed: %s", n, strings.Join(changed, ", "))
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// those names the files changed: "that file", "those files", or "what changed" when they are not known.
func those(changed []string, more int) string {
	switch n := len(changed) + more; {
	case n == 0:
		return "what changed"
	case n == 1:
		return "that file"
	}
	return "those files"
}

// OutsideScript is baton's question about a gate that runs scripts lying outside the repository (paths, as
// the command has them).
func OutsideScript(paths []string) string {
	return fmt.Sprintf("A script outside the repository: `%s` %s in no commit, so what the gate runs can change between runs, "+
		"and nobody can review it or run it again. Commit it in the repository first (a scripts folder), and verify that.",
		strings.Join(paths, "`, `"), plural(len(paths), "is", "are"))
}

// Unstaged is baton's question about a long gate whose earlier run, job prev, which took took, left no stage
// marker in its log.
func Unstaged(prev state.Job, took time.Duration) string {
	return fmt.Sprintf("Not built from stages: job %s ran it for %s, and its log has no stage marker, so a red run can only be run again whole. "+
		"Build the gate as a committed script of `baton stage <name> -- <command>` steps: it stops at the first stage that fails, "+
		"and `baton verify --failed` then reruns only what failed, carrying the stages that passed.", prev.ID, Span(took))
}

// LongBeforeQA is baton's question about a long gate queued in phase before the plan's QA phase qa ("P15
// (QA)"), QAAsk folded in: last is how long its command's latest run took (0: none long), and suite is set
// when it runs a whole suite.
func LongBeforeQA(phase, qa, command string, last time.Duration, suite bool) string {
	var why string
	switch {
	case last > 0 && suite:
		why = "it runs a whole suite, and its last run took " + Span(last)
	case last > 0:
		why = "its last run took " + Span(last)
	default:
		why = "it runs a whole suite"
	}
	return fmt.Sprintf("A long gate before QA: %s, and %s is not a QA phase. Phases run scoped checks that finish in seconds or minutes, "+
		"the tests of what the phase changed, and %s at the end runs the long ones, once. Is something in particular calling for it now? "+
		"If not, leave it to %s: its time is the human's. %s", why, phase, qa, qa, qaFlag(command))
}

// QAAskOnce is QAAsk for a gate, put with baton's other questions about it before it is queued.
func QAAskOnce(phase, qa, command string) string {
	return fmt.Sprintf("Is this a focused check of what %s changed, or a full suite, lint or end-to-end run that %s runs anyway? "+
		"If it is the latter and nothing in particular calls for it now, leave it to %s: its time is the human's. %s", phase, qa, qa, qaFlag(command))
}

// qaFlag is how a full check run early on purpose is flagged.
func qaFlag(command string) string {
	return "A full check you run early on purpose is flagged: baton verify --qa \"<why now>\" --notes \"…\" -- " + command + "."
}
