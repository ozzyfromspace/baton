package state

import (
	"cmp"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A rerun says why it runs a stage whole, and how long that stage ran last time (v0.8.15): a real run's
// `--failed` reran a browser stage of 1h18m and more whole three times, its queue message saying only "run
// whole", because baton read no failure ids from it. The model and the human see each whole stage's reason
// and cost before it runs (decide.RerunQueued), and a long red one is questioned once (cli/wholerun.go).

// Why a rerun runs a stage whole (WholeStage.Why), beside a failed stage's own reason (lane.Stage.Unnarrowed)
// and a skipped one's (WholeSkipped).
const (
	// WholeNotOnRecord: a gate the rerun draws on showed a failure in the stage and never ended it, so no
	// record says how it went (mergeStages).
	WholeNotOnRecord = "not on record"
	// WholeBroughtBack: --stages named it, so it runs whole whatever its record says (RerunRecord).
	WholeBroughtBack = "brought back by --stages"
)

// WholeSkipped is why a rerun runs whole a stage skipped in job, where an earlier stage stopped the gate.
func WholeSkipped(job string) string {
	if job == "" {
		return "skipped"
	}
	return "skipped in job " + job
}

// WholeStage is a stage a rerun runs whole (RerunView.Whole).
type WholeStage struct {
	Name string
	// Why it runs whole: a failed stage's Unnarrowed, WholeNotOnRecord, WholeSkipped or WholeBroughtBack.
	Why string
	// Took is the stage's last run (lane.Stage.Took) in the record the rerun runs from; 0 when unknown.
	Took time.Duration
}

// wholeStage is stage s of a rerun's gate (Rerun.Stages) as the rerun runs it whole, kept telling whether
// the record it runs from still has s: one it leaves out was brought back (--stages).
func wholeStage(s lane.Stage, kept bool) WholeStage {
	w := WholeStage{Name: s.Name, Took: s.Took()}
	switch {
	case !kept:
		w.Why = WholeBroughtBack
	case s.Status == lane.StageSkipped:
		w.Why = WholeSkipped(s.From)
	case s.Status == lane.StageFailed:
		w.Why = cmp.Or(s.Unnarrowed, lane.UnnarrowedNoIDs) // a record older than Unnarrowed, with no failures
	default:
		w.Why = WholeNotOnRecord
	}
	return w
}

// unrecorded are the stages the gates of from showed a failure in and never ended (Job.Failing), which
// stages, the record merged from them, no longer has (mergeStages): a rerun runs each whole, with no time
// on record.
func unrecorded(from []Job, stages []lane.Stage) []WholeStage {
	var out []WholeStage
	for _, j := range from {
		f := j.Failing
		if f == nil || f.Stage == "" || slices.ContainsFunc(stages, func(s lane.Stage) bool { return s.Name == f.Stage }) ||
			slices.ContainsFunc(out, func(w WholeStage) bool { return w.Name == f.Stage }) {
			continue
		}
		out = append(out, WholeStage{Name: f.Stage, Why: WholeNotOnRecord})
	}
	return out
}
