package state

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A plan may name a record file (plan.Plan.Record): baton appends each phase's record to it as the phase is
// done (a lead phase) or lands (an agent's), and commits the file alone in the lead's checkout (package
// loop, record.go). State.Records is what it wrote, for the run's plan: a new plan records its phases
// afresh, while a revision of it (Revise) keeps them.

// RecordNote is one phase's record that baton wrote to the plan's record file (Path, relative to the
// project's root), and the commit that holds it ("" where the project has no git). Told says the lead's
// model was told of it (TakeRecords).
type RecordNote struct {
	Phase  string    `json:"phase"`
	Path   string    `json:"path"`
	Commit string    `json:"commit,omitempty"`
	At     time.Time `json:"at"`
	Told   bool      `json:"told,omitempty"`
}

// HasRecord reports whether phase's record is written.
func (st State) HasRecord(phase string) bool {
	return slices.ContainsFunc(st.Records, func(n RecordNote) bool { return n.Phase == phase })
}

// Unrecorded lists the phases of p whose record is still to be written, in the order they were done
// (DoneAt; the plan's order between two done at once): every phase done, whether by the lead or by landing,
// that has none. It is none for a plan that names no record file.
func (st State) Unrecorded(p plan.Plan) []string {
	if p.Record == "" {
		return nil
	}
	var out []string
	for _, ph := range p.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == PhaseDone && !st.HasRecord(ph.ID) {
			out = append(out, ph.ID)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return st.Phases[a].DoneAt.Compare(st.Phases[b].DoneAt) })
	return out
}

// RecordCommits are the commits baton made to the record file: never a phase's own work.
func (st State) RecordCommits() []string {
	var out []string
	for _, n := range st.Records {
		if n.Commit != "" {
			out = append(out, n.Commit)
		}
	}
	return out
}

// TakeRecords returns the records the lead's model has not been told of, and marks them told. None is told
// while a compaction is owed or underway: the model hears of them after it.
func TakeRecords(st *State) []RecordNote {
	if st.Compacting() {
		return nil
	}
	var out []RecordNote
	for i := range st.Records {
		if n := &st.Records[i]; !n.Told {
			n.Told = true
			out = append(out, *n)
		}
	}
	return out
}
