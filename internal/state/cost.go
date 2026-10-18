package state

import (
	"slices"
	"time"
)

// Cost is what a phase has spent in gates so far, in clock time: its gate runs, the time they ran, and its
// time since it started, with the runs and the time of one command among them.
type Cost struct {
	Runs  int           // the phase's gate runs that started
	Gates time.Duration // the time they ran, a stretch two of them ran at once counted once
	Phase time.Duration // the phase's time since it started
	// Of is the command the cost was read for, and Command and CommandTime its runs alone and the time they
	// ran.
	Of          string
	Command     int
	CommandTime time.Duration
}

// GateCost is what phase has spent in gates in run st by now, and in command's gates among them. A gate
// runs from when its command began (Job.Ran, else Started) to its end, or now while it runs; one queued
// that never ran is not a run. Waits on something outside (await jobs) are not gates, nor are the merge
// queue's re-verifications (Job.Requeue), which are baton's, not the phase's. The phase's time runs from
// its start (PhaseState.StartedAt, or for an agent's phase its agent's start, Worker.Started), or from
// its first gate run if that came first: a phase taken up again starts again, its earlier gates still
// its own.
func GateCost(st State, phase, command string, now time.Time) Cost {
	c := Cost{Of: command}
	var all, its []span
	for _, j := range st.Jobs {
		if j.Phase != phase || j.Kind == JobAwait || j.Requeue {
			continue
		}
		from := j.Ran
		if from.IsZero() {
			from = j.Started
		}
		if from.IsZero() {
			continue
		}
		to := j.Ended
		if to.IsZero() {
			to = now
		}
		if to.Before(from) {
			to = from
		}
		s := span{from, to}
		c.Runs++
		all = append(all, s)
		if command != "" && j.Command == command {
			c.Command++
			its = append(its, s)
		}
	}
	c.Gates, c.CommandTime = covered(all), covered(its)
	start := phaseStart(st, phase)
	for _, s := range all {
		if start.IsZero() || s.from.Before(start) {
			start = s.from
		}
	}
	if !start.IsZero() && now.After(start) {
		c.Phase = now.Sub(start)
	}
	return c
}

// phaseStart is when phase started in run st: its agent's start for an agent's phase, else its own, or
// zero when the record has neither.
func phaseStart(st State, phase string) time.Time {
	ps := st.Phases[phase]
	if ps == nil {
		return time.Time{}
	}
	if ps.Worker != "" {
		for i := len(st.Workers) - 1; i >= 0; i-- {
			if w := st.Workers[i]; w.Phase == phase && w.Ref == ps.Worker && w.Generation == st.Generation && !w.Started.IsZero() {
				return w.Started
			}
		}
	}
	return ps.StartedAt
}

type span struct{ from, to time.Time }

// covered is the time the spans cover, a stretch two of them cover counted once.
func covered(ss []span) time.Duration {
	slices.SortFunc(ss, func(a, b span) int { return a.from.Compare(b.from) })
	var d time.Duration
	var end time.Time
	for _, s := range ss {
		if s.to.After(end) {
			from := s.from
			if end.After(from) {
				from = end
			}
			d += s.to.Sub(from)
			end = s.to
		}
	}
	return d
}
