package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A block (baton blocked) stops its phase for the human, but not for long: once nobody has answered it for
// Timing.BlockLook, baton lifts it, and the model looks again for a safe way on that drops no work, which
// it proposes before it takes it (decide.LookAgainRule). If it blocks again, the same happens again. A
// human answer first works as it always did.

// blockLook is how long a block waits for the human with no word from them.
func (t Timing) blockLook() time.Duration {
	if t.BlockLook > 0 {
		return t.BlockLook
	}
	return decide.BlockLook
}

// lookAgain lifts the lead's block once nobody has answered it for Timing.BlockLook, counted from the
// block or the human's last word, whichever is later, and not while the machine slept or the run is
// paused. Where its question is on screen, the model's call waits on that dialog, so the block is only
// marked lapsed (Block.Lapsed) and the dialog answered Look again: by baton's mod (hooks.Bound), or where
// it does not run, by answerOwnQuestion's dismissal; that answer lifts it (hooks' answered). Otherwise
// (the model never asked it, its dialog went away, or the human wrote and it was not asked again) the
// loop lifts it itself: announced, logged block_lapsed by timeout, pushed, and told to the model, which is
// woken (deliver). A phase agent's block is put to the human by the coordinator, and lifted the same way
// at its question's deadline (Coordinator.relay).
func (l *Loop) lookAgain(v host.View, st state.State) {
	if l.Store.Worker() != "" || !l.blockLapsed(v, st) {
		return
	}
	since := st.Blocked.Since
	if e := st.Run.Escalation; e != nil && e.Kind == "blocked" && e.Question != "" && st.Run.Dialogs.Has("escalation") {
		if !st.LookDue() {
			l.update(func(st *state.State) {
				if b := st.Blocked; b != nil && b.Since.Equal(since) && !st.LookDue() {
					b.Lapsed = v.Now
				}
			})
		}
		return
	}
	after := l.Timing.blockLook()
	var reason, phase string
	lifted := false
	l.update(func(st *state.State) {
		if b := st.Blocked; b == nil || !b.Since.Equal(since) || !l.blockLapsed(v, *st) {
			return // the human answered it meanwhile
		}
		reason, lifted = state.LiftBlock(st)
		phase = st.Current
		say, tell := decide.LookingAgain("", reason, phase, after, l.Timing.EscalationTimeout, true)
		st.Run.Announce = append(st.Run.Announce, say)
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "block_lapsed", Text: reason, At: v.Now})
		state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: tell}, v.Now)
	})
	if lifted {
		l.Store.Event("block_lapsed", map[string]any{"phase": phase, "reason": reason, "by": state.ByTimeout, "after": after.String(), "via": "loop"})
	}
}

// blockLapsed reports a block of the lead's that nobody has answered for Timing.BlockLook, counted from the
// block or the human's last word, whichever is later, while the plan runs.
func (l *Loop) blockLapsed(v host.View, st state.State) bool {
	b := st.Blocked
	return b != nil && st.Mode == state.ModeRunning && !v.Now.Before(l.since(latest(b.Since, st.Run.HumanAt)).Add(l.Timing.blockLook()))
}

// relayDue is when phase agent question r gets its default if nobody has answered it, counted from when
// it was queued, and whether it does even unasked, or with its dialog gone: a timed proposal goes ahead at
// its wait, a block is lifted after BlockLook for the agent to look again, and a question about a stopped
// agent, or one baton restarted, is decided from the record after the escalation timeout (stopped.go).
// Anything else waits for the human: a review, and the question with no deadline about a phase whose
// agent baton took down.
func (c *Coordinator) relayDue(r state.Relay) (time.Time, bool) {
	switch {
	case r.Timed():
		return r.Due(c.Lead.Timing.EscalationTimeout), true
	case r.Kind == state.RelayBlocked:
		return r.Due(c.Lead.Timing.blockLook()), true
	case r.AboutAgent() && !r.Persistent():
		return r.Due(c.Lead.Timing.EscalationTimeout), true
	}
	return time.Time{}, false
}

// liftRelayBlock lifts, in the agent's sub-run st, the block phase agent question r asked about, for the
// agent to look again, and tells it so, at its next tool call or stop, or woken. auto says nobody answered
// it (it is pushed). It reports false when the block was settled there meanwhile.
func liftRelayBlock(st *state.State, r state.Relay, t Timing, auto bool, now time.Time) bool {
	if !st.Waits(r) {
		return false
	}
	reason, _ := state.LiftBlock(st)
	_, tell := decide.LookingAgain(r.Phase, reason, r.Phase, t.blockLook(), t.EscalationTimeout, auto)
	if auto {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "block_lapsed", Text: reason, At: now})
	}
	state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: tell, About: r.Key()}, now)
	return true
}

// lookHold lifts the block phase agent question r held for the human (an answer with no way on: Wait for
// me, Pause baton) once nobody has written for Timing.BlockLook (lapseHolds), in the agent's sub-run, which
// tells the agent to look again, and records it in the run, which tells the human. Settled there
// meanwhile, or with the agent gone, there is nothing to lift: it is only marked done with.
func (c *Coordinator) lookHold(now time.Time, s *state.Store, st state.State, r state.Relay) {
	t := c.Lead.Timing
	lifted := false
	if sub := c.subStore(st, r.Phase); sub != nil {
		if _, err := sub.Update(func(sst *state.State) error { lifted = liftRelayBlock(sst, r, t, true, now); return nil }); err != nil {
			return // tried again at the next step
		}
	}
	s.Update(func(st *state.State) error {
		if q := st.RelayOf(r); q != nil && q.Answered != nil && q.Lapsed.IsZero() {
			q.Lapsed = now
		}
		if lifted {
			say, _ := decide.LookingAgain(r.Phase, r.Because, r.Phase, t.blockLook(), t.EscalationTimeout, true)
			st.Run.Announce = append(st.Run.Announce, say)
		}
		return nil
	})
	if lifted {
		s.Event("block_lapsed", relayFields(r, map[string]any{"by": state.ByTimeout, "after": t.blockLook().String(), "via": "hold"}))
	}
}
