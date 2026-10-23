package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole rerun says why and what it costs (v0.8.15): a real run's `--failed` reran a browser stage of 1h18m
// and more whole three times, its queue message saying only "run whole", because baton read no failure ids
// from it. Each stage a rerun runs whole is named with its last time and why (RerunQueued), and a red one
// that took GateLong or more is questioned once (cli/wholerun.go), as a long repeat is: the model reruns
// what failed by name, or says why it runs whole, and the human is told its reason (WholeRerunSaid), or that
// it gave none (WholeRerunUnexplained).

// GateWholeRerun is the kind of question (gate_questioned's type) put to a --failed rerun that would run
// whole a red stage that took GateLong or more last time.
const GateWholeRerun = "whole_rerun"

// wholeStages names stages a rerun runs whole, each with its last time, where known, and why: "`drives`
// (1h18m last time; baton read no failure ids from it)".
func wholeStages(stages []state.WholeStage) string {
	out := make([]string, len(stages))
	for i, w := range stages {
		var about []string
		if w.Took > 0 {
			about = append(about, Span(w.Took)+" last time")
		}
		if why := wholeWhy(w.Why); why != "" {
			about = append(about, why)
		}
		out[i] = "`" + w.Name + "`"
		if len(about) > 0 {
			out[i] += " (" + strings.Join(about, "; ") + ")"
		}
	}
	return strings.Join(out, "; ")
}

// wholeWhy is why a rerun runs a stage whole (state.WholeStage.Why), as the model and the human read it.
func wholeWhy(why string) string {
	switch why {
	case lane.UnnarrowedNoIDs:
		return "baton read no failure ids from it"
	case lane.UnnarrowedMany:
		return "it had " + why
	case lane.UnnarrowedWhole:
		return "a file or package of it failed with no test named"
	case lane.UnnarrowedRunners:
		return "its failures came from more than one runner"
	case state.WholeNotOnRecord:
		return "a run of it showed a failure and never ended, so baton has no record of it"
	}
	return why // skipped in job X, brought back by --stages
}

// wholeTook is how long stages took last time, together.
func wholeTook(stages []state.WholeStage) time.Duration {
	var d time.Duration
	for _, w := range stages {
		d += w.Took
	}
	return d
}

// wholeNames is "`a`" or "`a` and `b`": the stages' names.
func wholeNames(stages []state.WholeStage) string {
	names := make([]string, len(stages))
	for i, w := range stages {
		names[i] = "`" + w.Name + "`"
	}
	return andList(names)
}

// WholeRerunQuestioned is the refusal, once, of the --failed rerun of of, phase's gate, that would run whole
// red stages that took GateLong or more last time (long). logs are the logs of the gates they went red in,
// and c is phase's gates read for of's command (state.GateCost). Queued again with --why, it runs, and the
// human is told the reason (WholeRerunSaid); queued again without one, it runs too, flagged
// (WholeRerunUnexplained).
func WholeRerunQuestioned(phase string, of state.Job, long []state.WholeStage, logs []string, c state.Cost) string {
	var b strings.Builder
	b.WriteString("not queued, this once.")
	if lead := GateLead(phase, wholeTook(long), c); lead != "" {
		b.WriteString(" " + lead)
	}
	fmt.Fprintf(&b, "\n--failed reruns what failed in job %s (`%s`), and would run %s whole: %s.\n",
		of.ID, of.Command, plural(len(long), "this stage", "these stages"), wholeStages(long))
	fmt.Fprintf(&b, "The log of the run %s went red in may name what failed (%s, or a log the stage writes of its own): "+
		"if it does, rerun those by name with `baton verify --replace --notes \"…\" -- <a command naming them>`, "+
		"which makes that command %s's gate in place of %s, and the human is told. ",
		plural(len(long), "it", "they"), andList(logs), phase, of.ID)
	fmt.Fprintf(&b, "To run %s whole anyway, give the same call again with `--why \"…\"`: baton tells the human your reason.", plural(len(long), "it", "them"))
	return b.String()
}

// WholeRerunSaid tells the human that phase reruns red stages of its gate whole over baton's question
// (long, as WholeRerunQuestioned), with the model's reason: "P11 reruns `drives` of `bin/qa-set` whole
// (1h18m last time, run 4): <why>". c is phase's gates read for the gate's command before this run.
func WholeRerunSaid(phase string, long []state.WholeStage, c state.Cost, why string) string {
	return fmt.Sprintf("%s reruns %s of `%s` whole (%s): %s", phase, wholeNames(long), c.Of, lastRun(wholeTook(long), c, false), why)
}

// WholeRerunUnexplained tells the human that phase reran red stages of its gate whole over baton's question
// without saying why: "P11 reran `drives` of `bin/qa-set` whole (1h18m last time, run 4, 2h40m so far)
// without saying why". long and c are as for WholeRerunSaid.
func WholeRerunUnexplained(phase string, long []state.WholeStage, c state.Cost) string {
	return fmt.Sprintf("%s reran %s of `%s` whole (%s) without saying why", phase, wholeNames(long), c.Of, lastRun(wholeTook(long), c, true))
}

// lastRun is runOf with its time as the last: "1h18m last time, run 4".
func lastRun(took time.Duration, c state.Cost, so bool) string {
	return Span(took) + " last time" + strings.TrimPrefix(runOf(took, c, so), Span(took))
}
