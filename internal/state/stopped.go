package state

import "time"

// A phase agent the human stopped (x on its row in the agent panel, Ctrl-C at the prompt, Esc in its
// view) refuses every message after, and only the human can say what they want of its phase. baton asks
// them (a stopped question: Restart it?), and answers for them at its deadline from the record alone
// (Satisfied); after a second stop nobody answered, it takes the agent down and asks with no deadline
// (loop/stopped.go). The worker's record (Worker.Stops and the times beside it) keeps where that stands.

// AboutAgent reports a question about the phase's agent itself, kept in the run's roster rather than in
// the agent's sub-run: the human stopped it, or baton restarted it.
func (r Relay) AboutAgent() bool { return r.Kind == RelayStopped || r.Kind == RelayRestarted }

// Persistent reports the question baton never answers for the human: what becomes of a phase whose agent
// they stopped twice, which baton took down with the phase not done.
func (r Relay) Persistent() bool { return r.Kind == RelayStopped && r.Persist != "" }

// AgentWaits reports question r, about the agent on its phase (AboutAgent), still waiting for its answer
// in the run st: the phase's worker is still held for the stop it asks about (the take-down, for the
// persistent one), or, for a restart, still works with the agent baton started then.
func (st *State) AgentWaits(r Relay) bool {
	w := st.WorkerOn(r.Phase)
	if w == nil {
		return false
	}
	switch {
	case r.Persistent():
		return w.Status == WorkerHeld && w.TakenDown.Equal(r.Since)
	case r.Kind == RelayStopped:
		return w.Status == WorkerHeld && w.TakenDown.IsZero() && w.StoppedAt.Equal(r.Since)
	case r.Kind == RelayRestarted:
		return w.Status != WorkerHeld && w.Restarted.Equal(r.Since)
	}
	return false
}

// StopAsked reports a question about the stopped agent on phase that waits for the human's answer in the
// run st, which baton answers at its deadline: whether to restart it, unanswered or its answer not done
// yet.
func (st *State) StopAsked(phase string) bool {
	for _, r := range st.Relay {
		if r.Phase == phase && r.Kind == RelayStopped && !r.Persistent() && r.Applied.IsZero() && st.AgentWaits(r) {
			return true
		}
	}
	return false
}

// StopAgent records that the human stopped agent, the phase agent of worker e in the run st, at now: the
// worker is held, its stops counted, and a question queued for the human about it (title: its phase's).
// A refusal of the agent already counted is no new stop. It reports the stops counted, and whether this
// one was new.
func StopAgent(st *State, e Worker, agent, title string, now time.Time) (int, bool) {
	w := st.Entry(e)
	if w == nil || !w.Live() || w.Status == WorkerLanded || agent != "" && w.Stopped == agent && w.Status == WorkerHeld {
		return 0, false
	}
	w.Stops++
	w.Status, w.Since, w.Why = WorkerHeld, now, "the human stopped its agent"
	w.StoppedAt, w.Stopped, w.Released, w.TakenDown, w.Merge = now, agent, time.Time{}, time.Time{}, nil
	QueueRelay(st, Relay{Phase: w.Phase, Kind: RelayStopped, Since: now, Count: w.Stops, What: title}, now)
	return w.Stops, true
}

// Satisfied reports, from the record alone, that worker w needs no agent for its phase any more: its work
// landed (its phase is done in the run st), or in its sub-run sub the phase is done, or verifying with no
// red verdict on its latest job.
func Satisfied(st, sub State, w Worker) bool {
	if w.Status == WorkerLanded {
		return true
	}
	if ps := st.Phases[w.Phase]; ps != nil && ps.Status == PhaseDone {
		return true
	}
	ps := sub.Phases[w.Phase]
	switch {
	case ps == nil:
		return false
	case ps.Status == PhaseDone:
		return true
	case ps.Status == PhaseVerifying:
		j := sub.Latest(w.Phase)
		return j == nil || !j.Red()
	}
	return false
}
