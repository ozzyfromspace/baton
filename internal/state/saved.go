package state

import "time"

// Saving is the time phase agents saved a plan, from the run's record: the phases' working time, summed,
// against the wall clock from the first phase's start to the plan's completion. A phase the lead worked on
// counts from its start to its done. A phase an agent worked on counts from its agent's start
// (Worker.Started) to its finish (Worker.Finished, when its sub-run was complete), not to its landing,
// which waits on the merge queue and the lead. The wall clock runs on through every wait of the run, its
// landings, compactions and idle stretches among them, so what it counts as saved is never more than
// the work done side by side.
type Saving struct {
	Work   time.Duration // the phases' working time, summed
	Wall   time.Duration // from the first phase's start to the last phase's done
	Agents int           // the phases phase agents worked on
}

// Saved is how much less the wall clock took than the phases' working time, never below zero.
func (s Saving) Saved() time.Duration { return max(0, s.Work-s.Wall) }

// Log adds the numbers to an event's fields, in whole seconds, and returns them.
func (s Saving) Log(fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["agent_phases"] = s.Agents
	fields["work_seconds"] = int(s.Work.Seconds())
	fields["wall_seconds"] = int(s.Wall.Seconds())
	fields["saved_seconds"] = int(s.Saved().Seconds())
	return fields
}

// SavingOf reads what phase agents saved run st's plan from its record, over the phases done: a plan
// complete has them all. A phase with no start on record counts no work, and starts no wall clock.
func SavingOf(st State) Saving {
	var s Saving
	var first, last time.Time
	for id, ps := range st.Phases {
		if ps == nil || ps.Status != PhaseDone || ps.DoneAt.IsZero() {
			continue
		}
		if ps.DoneAt.After(last) {
			last = ps.DoneAt
		}
		from, to := ps.StartedAt, ps.DoneAt
		if ps.Worker != "" {
			s.Agents++
			if w := st.landedOn(id, ps.Worker); w != nil {
				from, to = agentSpan(*w, from, to)
			}
		}
		if from.IsZero() {
			continue
		}
		for _, t := range []time.Time{from, ps.StartedAt} {
			if !t.IsZero() && (first.IsZero() || t.Before(first)) {
				first = t
			}
		}
		if to.After(from) {
			s.Work += to.Sub(from)
		}
	}
	if !first.IsZero() && last.After(first) {
		s.Wall = last.Sub(first)
	}
	return s
}

// landedOn is the roster's entry of this plan whose work on phase landed, from the sub-run ref, or nil.
func (st State) landedOn(phase, ref string) *Worker {
	for i := len(st.Workers) - 1; i >= 0; i-- {
		if w := &st.Workers[i]; w.Phase == phase && w.Ref == ref && w.Status == WorkerLanded && w.Generation == st.Generation {
			return w
		}
	}
	return nil
}

// agentSpan is when worker w's agents worked on its phase, which the run's record has from assigned to
// landed: from its first agent's start (its assignment, for a record without one) to its finish (its
// landing, for a record without one).
func agentSpan(w Worker, assigned, landed time.Time) (from, to time.Time) {
	from, to = assigned, landed
	switch {
	case !w.Started.IsZero():
		from = w.Started
	case !w.Assigned.IsZero():
		from = w.Assigned
	}
	if !w.Finished.IsZero() && !w.Finished.After(landed) {
		to = w.Finished
	}
	return from, to
}
