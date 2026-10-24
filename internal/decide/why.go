package decide

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A repeat states its reason (v0.8.11): a gate baton questions runs anyway when it is queued with `--why`,
// the model's word on what this run can show that the last could not, and the human is told that reason.
// A repeat queued again with none still runs, since nothing is refused twice, but the human is told that
// too, and the job's result opens with it (cli/proportion.go). A silent resubmit was the cheapest answer to
// baton's question; now the reason costs a sentence, and its absence is seen.

// GateLead opens baton's questions about a gate: what it spends of the human's time (TimeShort, from took,
// how long the earlier run they draw on took; nothing for 0), and what phase has spent on gates so far
// (GateCost, from c).
func GateLead(phase string, took time.Duration, c state.Cost) string {
	var parts []string
	if took > 0 {
		parts = append(parts, TimeShort(took))
	}
	if s := GateCost(phase, c); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// RepeatSaid tells the human that phase reruns a long gate over baton's question, with the model's reason:
// "P53 reruns `make test-degraded` (12m, run 8): <why>". took is how long the command's last run took, and c
// the phase's gates read for the command before this run.
func RepeatSaid(phase string, took time.Duration, c state.Cost, why string) string {
	return fmt.Sprintf("%s reruns `%s` (%s): %s", phase, c.Of, runOf(took, c, false), why)
}

// RepeatUnexplained tells the human that phase reran a long gate over baton's question without saying why:
// "P53 reran `make test-degraded` (12m, run 8, 1h20m so far) without saying why". took and c are as for
// RepeatSaid.
func RepeatUnexplained(phase string, took time.Duration, c state.Cost) string {
	return fmt.Sprintf("%s reran `%s` (%s) without saying why", phase, c.Of, runOf(took, c, true))
}

// runOf is "12m, run 8", and with so ", 1h20m so far": how long the last run took, which run of the command
// in its phase this one is, and the time its runs there took so far. A command that never ran in the phase
// (its last run was another phase's) has neither.
func runOf(took time.Duration, c state.Cost, so bool) string {
	parts := []string{Span(took)}
	if c.Command > 0 {
		parts = append(parts, fmt.Sprintf("run %d", c.Command+1))
		if so && c.CommandTime > 0 {
			parts = append(parts, Span(c.CommandTime)+" so far")
		}
	}
	return strings.Join(parts, ", ")
}

// GateAnswered tells the human that phase runs gate command though baton questions it, kinds being the
// questions (not GateRepeat: RepeatSaid says that), with the model's reason.
func GateAnswered(phase, command string, kinds []string, why string) string {
	var about []string
	for _, k := range kinds {
		switch k {
		case GateOutside:
			about = append(about, "it runs a script in no commit")
		case GateUnstaged:
			about = append(about, "it is not built from stages")
		case GateEnvAgain:
			about = append(about, envAgainAbout)
		case GateBeforeQA, GateQAAsk:
			if !slices.Contains(about, "a full check before QA") {
				about = append(about, "a full check before QA")
			}
		}
	}
	return fmt.Sprintf("%s runs `%s` over baton's question (%s): %s", phase, command, strings.Join(about, "; "), why)
}

// HumanTold is what the model hears of a line baton told the human about its gate.
func HumanTold(line string) string { return "baton told the human: " + line + "." }

// unexplainedFirst opens the result of a gate queued again over baton's question with no reason
// (Job.Unexplained), or is "".
func unexplainedFirst(j state.Job) string {
	if j.Unexplained == "" {
		return ""
	}
	return "[baton] " + j.Unexplained + "; the human was told. A rerun queued with --why says what it can show that the last could not.\n"
}

// onlyFailed offers the rerun of only what failed in a red Prev: `baton verify --failed` for a gate built
// from stages, else the tests its log names (Failures), in its runner's own command where every one has a
// runner and all are listed. It is "" when baton knows of neither.
func (r Repeat) onlyFailed() string {
	p := r.Prev
	switch {
	case r.Failed:
		return "`baton verify --failed --notes \"…\"`, which reruns only what failed in " + p.ID + ", carrying the stages that passed"
	case len(r.Failures) == 0:
		return ""
	}
	ids := make([]string, len(r.Failures))
	for i, f := range r.Failures {
		ids[i] = f.ID
	}
	s := fmt.Sprintf("only what failed in %s (%s", p.ID, strings.Join(ids, ", "))
	if r.MoreFailures > 0 {
		s += fmt.Sprintf(" and %d more", r.MoreFailures)
	}
	s += ")"
	if r.MoreFailures > 0 || slices.ContainsFunc(r.Failures, func(f lane.FailedTest) bool { return f.Runner == "" }) {
		return s + ", by name"
	}
	return fmt.Sprintf("%s: `baton verify --failed --notes \"…\" -- %s`, and a pass clears the red gate", s, strings.Join(lane.Reruns(r.Failures), " && "))
}
