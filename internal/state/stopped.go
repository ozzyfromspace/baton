package state

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// A phase agent the human stopped (x on its row in the agent panel, Ctrl-C at the prompt, Esc in its
// view) refuses every message after, and only the human can say what they want of its phase. baton asks
// them (a stopped question: Restart it?), and answers for them at its deadline from the record alone
// (Satisfied); after a second stop nobody answered, it takes the agent down and asks with no deadline
// while the rest of the run can go on, and once nothing else can move, takes the phase back for the lead
// (StoppedTwice, Moves, TakeUpAt; loop/stopped.go). The worker's record (Worker.Stops and the times beside
// it) keeps where that stands.

// AboutAgent reports a question about the phase's agent itself, kept in the run's roster rather than in
// the agent's sub-run: the human stopped it, or baton restarted it.
func (r Relay) AboutAgent() bool { return r.Kind == RelayStopped || r.Kind == RelayRestarted }

// Persistent reports the question baton never answers for the human: what becomes of a phase whose agent
// they stopped twice, which baton took down with the phase not done. baton takes the phase back for the
// lead only once the run can go on no other way (TakeUpAt).
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
// red verdict on the jobs it verifies on (WaitsOn).
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
		for _, j := range sub.WaitsOn(w.Phase) {
			if j.Red() {
				return false
			}
		}
		return true
	}
	return false
}

// StoppedTwice is the question with no deadline (Persistent) about a phase whose agent the human stopped
// twice, which baton took down with the phase not done, that is open in the run st: the human has not
// answered it (it waits to be asked, is on their screen, or stepped aside to come back), and the phase
// still waits on it. Of several, the one queued first. nil for none.
func (st State) StoppedTwice() *Relay {
	for i := range st.Relay {
		r := &st.Relay[i]
		if !r.Persistent() || r.Answered != nil && (r.Answered.By == ByHuman || r.Answered.By == ByWithdrawn) {
			continue
		}
		if st.AgentWaits(*r) {
			return r
		}
	}
	return nil
}

// Moves says what else in the run st, of plan p, can still move at now while phase waits on the human's
// answer about its twice-stopped agent, or "" when nothing can: the run runs, the lead holds no phase, its
// turn is over, and no compaction, replan, question of the lead's own, usage limit or job is under way; no
// phase can start; and no other phase agent is at work, or held for a question baton answers at its
// deadline. Another phase that waits on the human as this one does (left stopped as they chose, or
// stopped twice) moves nothing. It is for the host's log and events: the words are decide's.
func (st State) Moves(p plan.Plan, phase string, now time.Time) string {
	switch {
	case st.Mode != ModeRunning:
		return "the run is " + st.Mode
	case st.Current != "":
		return "the lead holds " + st.Current
	case st.Run.TurnOpen:
		return "the lead's turn runs"
	case st.Run.Compaction.InFlight():
		return "a compaction is under way"
	case st.Replan != nil:
		return "a replan is open"
	case st.OwnQuestion():
		return "a question of the lead's waits"
	case st.Usage != nil && now.Before(st.Usage.Limited):
		return "a usage limit lifts at " + st.Usage.Limited.UTC().Format(time.RFC3339)
	}
	if jobs := st.Unfinished(); len(jobs) > 0 {
		return "job " + jobs[0].ID + " runs"
	}
	if next := Next(st, p); next != "" {
		return next + " can start"
	}
	if next := NextWorker(st, p); next != "" {
		return next + " can start"
	}
	for _, w := range st.LiveWorkers() {
		switch {
		case w.Phase == phase && w.Generation == st.Generation:
		case w.Status != WorkerHeld:
			return "the agent on " + w.Phase + " is at work"
		case st.StopAsked(w.Phase):
			return "the question about the agent on " + w.Phase + " has a deadline"
		}
	}
	return ""
}

// TakeUpAt is when baton takes the phase of r, the open question about a twice-stopped phase
// (StoppedTwice), back for the lead, with nothing else in the run st able to move (Moves) since since (the
// question's Stuck, as the host saw it; at the lead's stop, its stop): wait (escalation_timeout) after the
// latest of that, the lead's last turn, the human's last word and the question's queueing, and never
// before the time its dialog names (Deadline). Named as HH:MM, it is never sooner.
func (st State) TakeUpAt(r Relay, since time.Time, wait time.Duration) time.Time {
	from := since
	for _, t := range []time.Time{st.Run.TurnStarted, st.Run.LastStop, st.Run.HumanAt, r.Queued} {
		if t.After(from) {
			from = t
		}
	}
	at := from.Add(wait)
	if c := at.Truncate(time.Minute); c.Before(at) {
		at = c.Add(time.Minute)
	}
	if r.Deadline.After(at) {
		at = r.Deadline
	}
	return at
}
