package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// An API error that ended a turn because the API refused the conversation itself (too long for the model's
// context, a request too large, a tool history it cannot take: state.StopError.Afresh) would end every turn
// of that conversation, and needs no human: a fresh session from the brief avoids it. So the lead's loop
// ends its claude for one, which the host starts as it does when a conversation cannot be resumed
// (cli/recover.go), and a phase agent's phase is taken up by a fresh agent in its worktree, as after a
// checkpoint (Coordinator.erred). Each is a recovery of its run (Runtime.Recoveries): past
// state.MaxRecoveries in state.RecoveryWindow, it waits first (state.Backoff), and the human is told when it
// goes on. A refusal, a login, billing or a model it may not use stay the human's, as before (watchError).

// fresh is the fresh session the loop ended claude for: why its conversation could not go on
// (decide.FailedTurn), and how long the loop waited first.
type fresh struct {
	why    string
	waited time.Duration
}

// Afresh reports that the loop ended claude for a fresh session of the lead, why, and how long it waited
// first: the host then starts one from the phase's brief.
func (l *Loop) Afresh() (why string, waited time.Duration, ok bool) {
	if l.fresh == nil {
		return "", 0, false
	}
	return l.fresh.why, l.fresh.waited, true
}

// afresh takes an API error a fresh session avoids, e, which ended the last turn, and reports whether it
// did. A phase agent's is the coordinator's (erred): its loop neither retries nor escalates it. The lead's,
// where the host starts it afresh (StartsAfresh), waits out the backoff its recoveries owe, then ends its
// claude for a fresh session as soon as nothing would be lost to it: no dialog open, nobody typing, no
// background agent of the model's own at work (for at most BackgroundMax); a draft in the input box is
// saved where `baton drafts` hands it back. The phase agents end with the lead's claude, and the next host
// takes their phases up again in fresh agents. A turn that starts meanwhile (the human writing)
// supersedes all of this (watch).
func (l *Loop) afresh(v host.View, st state.State, in host.Injector, e state.StopError) bool {
	if l.Store.Worker() != "" {
		return true
	}
	if !l.StartsAfresh {
		return false
	}
	wait := state.Backoff(st.Run.Recoveries, e.At, l.Timing.RecoverAfter, l.Timing.RecoverEvery)
	due := l.since(e.At).Add(wait)
	why := decide.FailedTurn(e)
	if !l.freshSeen.Equal(e.At) {
		l.freshSeen = e.At
		l.Store.Event("afresh_due", map[string]any{"error": e.Error, "phase": st.Current, "wait": wait.String()})
		if wait > 0 {
			text := decide.AfreshLater(l.stopped(st), why, due, state.RecoveryWindow)
			l.update(func(st *state.State) {
				st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "afresh_later", Text: text, At: v.Now})
			})
		}
	}
	if v.Now.Before(due) {
		return true
	}
	switch {
	case l.why == gateDraft && v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff:
		return true // somebody is typing: they finish first
	case l.why != "" && l.why != gateDraft:
		l.held(v, st, "start the lead afresh")
		return true
	}
	if v.Draft {
		path, err := l.Store.SaveDraft(v.DraftText, st.Current, v.Now)
		if err != nil {
			l.logf("loop: saving the draft: %v", err)
			return true // never throw away text baton failed to save
		}
		if path != "" {
			l.Store.Event("draft_rescued", map[string]any{"chars": len(v.DraftText), "path": path})
		}
	}
	l.proceed()
	l.quit = true
	l.fresh = &fresh{why: why, waited: wait}
	l.Store.Event("lead_afresh", map[string]any{"error": e.Error, "phase": st.Current, "waited": wait.String()})
	l.logf("loop: ending claude for a fresh session: %s", why)
	if err := in.Quit(); err != nil {
		l.logf("loop: quitting: %v", err)
	}
	return true
}

// erred reports that the last turn of w's agent failed with an API error a fresh agent avoids
// (state.StopError.Afresh), and that the backoff its sub-run's recoveries owe is over (state.Backoff): its
// phase is then taken up by a fresh agent in its worktree (takeAfresh). Until then its agent is let be:
// nothing is sent to it, and its loop neither retries nor escalates the error (Loop.afresh). When it must
// wait, the human is told once, by a notice.
func (c *Coordinator) erred(now time.Time, w *worker) bool {
	if w.agent.in == nil || w.sending != 0 || w.ending != "" {
		return false
	}
	st, err := w.store.Load()
	if err != nil || st.Mode != state.ModeRunning || st.Run.TurnOpen {
		return false
	}
	e := st.Run.LastError
	if e == nil || !e.Afresh() || st.Run.TurnStarted.After(e.At) {
		return false
	}
	wait := state.Backoff(st.Run.Recoveries, e.At, c.Lead.Timing.RecoverAfter, c.Lead.Timing.RecoverEvery)
	due := e.At.Add(wait)
	if !w.erred.Equal(e.At) {
		w.erred = e.At
		w.store.Event("afresh_due", map[string]any{"agent": w.entry.AgentID, "error": e.Error, "phase": w.phase, "wait": wait.String()})
		if wait > 0 {
			text := decide.AfreshLater(decide.Stop{Phase: w.phase, Worker: true}, decide.FailedTurn(*e), due, state.RecoveryWindow)
			w.store.Update(func(st *state.State) error {
				st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "afresh_later", Text: text, At: now})
				return nil
			})
		}
	}
	return !now.Before(due)
}

// takeAfresh sets w's phase aside for a fresh agent, after its agent's last turn failed with an API error
// a fresh agent avoids (erred): its worker is suspended, saying why, and the recovery recorded in its
// sub-run, which counts them. The agent is then stopped and let go (afresh), and a fresh one takes the
// phase up in its worktree, as soon as an agent may start (step: respawn).
func (c *Coordinator) takeAfresh(now time.Time, s *state.Store, w *worker) {
	st, err := w.store.Load()
	if err != nil || st.Run.LastError == nil {
		return
	}
	e := *st.Run.LastError
	why := decide.FailedTurn(e)
	wait := state.Backoff(st.Run.Recoveries, e.At, c.Lead.Timing.RecoverAfter, c.Lead.Timing.RecoverEvery)
	w.store.Update(func(st *state.State) error {
		st.Run.Recoveries = append(state.Recent(st.Run.Recoveries, now), state.Recovery{At: now, Why: why, Fresh: true, Waited: wait})
		return nil
	})
	c.setEntry(s, w.entry, func(x *state.Worker) {
		if x.Live() {
			x.Status, x.Since, x.Why, x.Resume = state.WorkerSuspended, now, decide.AgentFailedTurn(why), ""
		}
	})
	w.store.Event("agent_afresh", map[string]any{"agent": w.entry.AgentID, "error": e.Error, "waited": wait.String()})
}
