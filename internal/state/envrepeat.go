package state

import (
	"slices"
	"time"
)

// Two whole runs of a phase's gate that lose a stage to the same kind push the human, once per phase and
// kind (v0.8.17, loop/envrepeat.go): the machine may be too small for it, which only they can change. The
// host reads each whole run once, as it ends (Job.EnvRead, Job.EnvLost), so the runs before it are on
// record when it does.

// EnvUnread are the whole runs in st that ended red or were stopped and whose losses the host has not read
// yet (Job.EnvRead), oldest first.
func EnvUnread(st State) []Job {
	var out []Job
	for _, j := range st.Jobs {
		if WholeRun(j) && (j.Red() || j.Status == JobInterrupted) && j.EnvRead.IsZero() {
			out = append(out, j)
		}
	}
	return out
}

// EnvRepeat is a push due to the human: phase's gate (Command) lost Stages to Kind in two whole runs, Jobs,
// the earlier first.
type EnvRepeat struct {
	Phase, Command, Kind string
	Stages               []string
	Jobs                 []string
}

// ReadEnv records lost, what the host read of whole run id's losses (EnvLost), on the job (EnvRead, at now),
// and returns the pushes it makes due: for each kind among lost, when an earlier whole run of the same
// phase and command, read before, lost a stage to it too, once per phase and kind (PhaseState.EnvPushed,
// which it records). A job read already, or gone from the record, makes none due.
func ReadEnv(st *State, id string, lost []EnvLoss, now time.Time) []EnvRepeat {
	j := st.Job(id)
	if j == nil || !j.EnvRead.IsZero() {
		return nil
	}
	j.EnvRead, j.EnvLost = now, lost
	ps := st.Phases[j.Phase]
	var due []EnvRepeat
	for _, kind := range lossKinds(lost) {
		if ps == nil || slices.Contains(ps.EnvPushed, kind) {
			continue
		}
		prev := earlierLoss(*st, *j, kind)
		if prev == nil {
			continue
		}
		ps.EnvPushed = append(ps.EnvPushed, kind)
		var stages []string
		for _, l := range append(slices.Clone(prev.EnvLost), lost...) {
			if l.Kind == kind && !slices.Contains(stages, l.Stage) {
				stages = append(stages, l.Stage)
			}
		}
		due = append(due, EnvRepeat{Phase: j.Phase, Command: j.Command, Kind: kind, Stages: stages, Jobs: []string{prev.ID, j.ID}})
	}
	return due
}

// lossKinds are the kinds of lost, each once, in their order.
func lossKinds(lost []EnvLoss) []string {
	var kinds []string
	for _, l := range lost {
		if !slices.Contains(kinds, l.Kind) {
			kinds = append(kinds, l.Kind)
		}
	}
	return kinds
}

// earlierLoss is the latest whole run of j's phase and command before j, read already, that lost a stage to
// kind; nil for none.
func earlierLoss(st State, j Job, kind string) *Job {
	var prev *Job
	for i := range st.Jobs {
		o := &st.Jobs[i]
		if o.ID == j.ID || o.Phase != j.Phase || o.Command != j.Command || !WholeRun(*o) || o.EnvRead.IsZero() || o.Ended.After(j.Ended) ||
			!slices.ContainsFunc(o.EnvLost, func(l EnvLoss) bool { return l.Kind == kind }) {
			continue
		}
		if prev == nil || o.Ended.After(prev.Ended) {
			prev = o
		}
	}
	return prev
}
