package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent the human stopped (x on its row in the agent panel, Ctrl-C at the prompt, Esc in its
// view) refuses every message after as "stopped by the user" (spike 36), and only the human can say what
// they want of its phase. The coordinator holds its worker (state.StopAgent: Worker.Stops counts the
// stops) and has the mod ask them, on the lead's screen, whether to restart it (a relay question,
// state.RelayStopped). The question waits escalation_timeout from when it was queued; then baton decides
// from the record alone (state.Satisfied, decide.AgentStopAction):
//
//   - first stop: an agent whose work is done (its phase done, or verifying with no red verdict) is let go,
//     and its work lands without one, its worker retired once it has; otherwise a fresh agent takes the
//     phase up in its worktree, and a second question tells the human so (state.RelayRestarted, which
//     times out to Keep it going).
//   - second stop: the same, but an agent whose work is not done is taken down, and a question with no
//     deadline asks the human what to do with the phase. Only that phase and the phases that need it wait.
//     Its dialog steps aside after escalation_timeout, so that it holds neither the lead nor the other
//     agents' questions, and comes back when the human next writes in the lead's session.
//
// The human's answer is done as they chose: Restart it starts a fresh agent (and on a second stop counts
// the stops afresh), Leave it stopped leaves the phase waiting for them (/baton run starts a fresh agent),
// Pause baton pauses the run.

// holdAgent holds w's phase for the human, who stopped its agent: a message to it was refused as stopped
// by the user. Nothing is sent to the agent again, its work stays in its worktree, and its loop goes on
// watching its jobs; the stop is counted, and the human asked whether to restart it. A refusal of an
// agent already counted changes nothing.
func (c *Coordinator) holdAgent(s *state.Store, b *bridge.Bridge, w *worker) {
	now := s.Now()
	w.agent.in = nil
	agent := c.agentOf(s, w)
	if agent != "" {
		b.Forget(agent)
	}
	title := ""
	if pl, err := s.LoadPlan(); err == nil {
		if i := pl.Index(w.phase); i >= 0 {
			title = pl.Phases[i].Title
		}
	}
	var stops int
	var fresh bool
	s.Update(func(st *state.State) error {
		stops, fresh = state.StopAgent(st, w.entry, agent, title, now)
		return nil
	})
	if !fresh {
		return
	}
	c.Lead.logf("loop: %s: the human stopped its agent (%d)", w.phase, stops)
	s.Event("agent_stopped", map[string]any{"phase": w.phase, "stops": stops, "agent": agent})
	s.Event("agent_restart_asked", map[string]any{"phase": w.phase, "stops": stops})
	c.announce(s, decide.AgentStopped(w.phase, stops))
}

// applyStop does what the answer to question r about the agent on its phase (state.Relay.AboutAgent)
// calls for, on the run's roster, and records that it did, in one write: a question applied twice does
// nothing more. One whose worker moved on meanwhile (/baton run took it up, its work landed) is only
// marked applied. What the human is told, the fresh agent too, comes after.
func (c *Coordinator) applyStop(now time.Time, s *state.Store, st state.State, r state.Relay) {
	satisfied := false
	if e := st.WorkerOn(r.Phase); e != nil {
		if sub := c.subStore(st, r.Phase); sub != nil {
			if sst, err := sub.Load(); err == nil {
				satisfied = state.Satisfied(st, sst, *e)
			}
		}
	}
	a := *r.Answered
	human := a.By == state.ByHuman
	action := decide.AgentStopAction(r, a, satisfied)
	var e state.Worker
	marked, acted := false, false
	s.Update(func(st *state.State) error {
		q := st.RelayOf(r)
		if q == nil || !q.Applied.IsZero() {
			return nil
		}
		q.Applied, marked = now, true
		x := st.WorkerOn(r.Phase)
		if x == nil || !st.AgentWaits(r) {
			return nil
		}
		acted = true
		notice := func(kind, text string) {
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: kind, Text: text, At: now})
		}
		switch action {
		case decide.StopRestart:
			x.Status, x.Since, x.Why, x.Resume = state.WorkerSuspended, now, decide.AgentRestartedWhy(human), ""
			x.Restarted, x.Released, x.TakenDown = now, time.Time{}, time.Time{}
			if human && r.Count >= 2 {
				x.Stops = 0 // the two-try loop starts over
			}
			if !human {
				state.QueueRelay(st, state.Relay{Phase: r.Phase, Kind: state.RelayRestarted, Since: now, What: r.What}, now)
				notice("agent_stopped", decide.AgentRestartedNotice(r.Phase))
			}
		case decide.StopLeave:
			x.Status, x.Since, x.Why, x.TakenDown = state.WorkerHeld, now, decide.AgentLeftWhy, time.Time{}
		case decide.StopPause:
			if state.Pause(st) == nil {
				st.Run.Announce = append(st.Run.Announce, decide.RelayPaused(r.Phase))
			}
		case decide.StopRelease:
			// Its work lands without an agent (the merge queue lands a working worker whose sub-run is
			// complete), and its worker is retired once it has; were its work to need an agent again, a
			// fresh one takes it up (watch). No agent of it is ever messaged again.
			x.Status, x.Since, x.Why, x.AgentID, x.Released = state.WorkerWorking, now, "", "", now
			notice("agent_stopped", decide.AgentReleasedNotice(r.Phase))
		case decide.StopTakeDown:
			x.Why, x.TakenDown = decide.AgentTakenDownWhy, now
			state.QueueRelay(st, state.Relay{Phase: r.Phase, Kind: state.RelayStopped, Since: now, Count: x.Stops, What: r.What,
				Persist: decide.AgentPersistent}, now)
			notice("worker_held", decide.AgentTakenDownNotice(r.Phase))
		}
		e = *x
		return nil
	})
	if !marked {
		return // applied already
	}
	fields := relayFields(r, map[string]any{"answer": a.Answer, "by": a.By, "result": action})
	if !acted {
		fields["result"] = "its worker moved on meanwhile"
	}
	s.Event("relay_applied", fields)
	if !acted {
		return
	}
	switch action {
	case decide.StopRestart:
		s.Event("agent_restarted", map[string]any{"phase": r.Phase, "by": a.By, "stops": e.Stops})
		c.announce(s, decide.AgentRestarted(r.Phase, human))
		if sub := c.subStore(st, r.Phase); sub != nil {
			sub.Update(func(sst *state.State) error {
				state.Tell(sst, state.Message{Kind: state.MessageAnswer, Text: decide.AgentRestartedTell(r.Phase, human), About: r.Key()}, now)
				return nil
			})
		}
	case decide.StopLeave:
		if r.Kind == state.RelayRestarted {
			if w := c.session(e); w != nil {
				c.stopFresh(s, w)
			}
			s.Event("worker_held", map[string]any{"phase": r.Phase, "by": a.By, "why": "the human stopped the agent baton restarted"})
			c.announce(s, decide.AgentStoppedAgain(r.Phase))
			return
		}
		s.Event("worker_held", map[string]any{"phase": r.Phase, "by": a.By, "why": decide.AgentLeftWhy})
		c.announce(s, decide.AgentLeft(r.Phase))
	case decide.StopPause:
		s.Event("paused", map[string]any{"by": "relay", "phase": r.Phase})
	case decide.StopRelease:
		s.Event("worker_retired", map[string]any{"phase": r.Phase, "by": a.By, "why": "its work is done", "lands": "without an agent"})
		c.announce(s, decide.AgentReleased(r.Phase))
	case decide.StopTakeDown:
		s.Event("agent_taken_down", map[string]any{"phase": r.Phase, "by": a.By, "stops": e.Stops, "pushed": true})
		c.announce(s, decide.AgentTakenDown(r.Phase))
	case decide.StopKeep:
		if human && a.Answer != decide.KeepItGoing {
			if sub := c.subStore(st, r.Phase); sub != nil {
				sub.Update(func(sst *state.State) error {
					state.Tell(sst, state.Message{Kind: state.MessageAnswer, Text: decide.AgentWordsTell(r.Phase, a.Answer), About: r.Key()}, now)
					return nil
				})
			}
		}
	}
}

// stopFresh stops the agent baton restarted for w, as the human chose: its spawn, if still open, is taken
// back, and its agent stopped, and none of baton's any more. Its worker is held (applyStop), its work kept
// in its worktree, and its loop goes on watching its jobs.
func (c *Coordinator) stopFresh(s *state.Store, w *worker) {
	b := c.bridge(s)
	w.agent.in = nil
	if w.spawn != 0 {
		b.Void(s.Instance, w.spawn)
		w.spawn = 0
	}
	if agent := c.agentOf(s, w); agent != "" {
		b.Issue(bridge.Command{Kind: bridge.Stop, Instance: s.Instance, Phase: w.phase, Agent: agent})
		b.Forget(agent)
	}
}

// stoppedAgain puts the question with no deadline about a phase whose agent baton took down back in the
// queue once the human is back: it stepped aside unanswered (its dialog's time ran out, or it went away),
// and they have written in the lead's session since.
func (c *Coordinator) stoppedAgain(now time.Time, s *state.Store, st state.State) {
	if st.Mode != state.ModeRunning {
		return
	}
	for _, r := range st.Relay {
		a := r.Answered
		if !r.Persistent() || a == nil || a.By == state.ByHuman || a.By == state.ByWithdrawn || r.Applied.IsZero() ||
			!st.Run.HumanAt.After(a.At) || !st.AgentWaits(r) {
			continue
		}
		again := false
		s.Update(func(st *state.State) error { again = state.AskAgain(st, r, now); return nil })
		if again {
			s.Event("relay_again", relayFields(r, map[string]any{"why": "the human is back"}))
		}
	}
}

// needsAgent reports that worker e, whose stopped agent baton let go with its work done (Released), has
// work that needs an agent again (its verification turned red, or its phase was taken up again), as its
// sub-run's record says: w, its session, has no agent, and none is starting.
func (c *Coordinator) needsAgent(st state.State, w *worker, e state.Worker) bool {
	if e.Status != state.WorkerWorking || e.Released.IsZero() || w.agent.in != nil || w.spawn != 0 || w.ending != "" {
		return false
	}
	sub, err := w.store.Load()
	return err == nil && !state.Satisfied(st, sub, e)
}
