package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase that goes badly is raised a step (escalate, escalate_after; docs/research/tiers.md): once its own
// gate has gone red Tiers.After times since its tier was last set (state.RedRuns: never a run that timed
// out or whose environment took a stage), its tier climbs one step (plan.Raise: effort to xhigh, then the
// next allowed family, then max), at most Tiers.Steps steps above its plan's. Only up, and only for the
// phase: the next phase starts on its own plan's tier. A phase whose plan names no tier is never raised: a
// plan from before tiers runs as it always did. A phase agent's tier is fixed for its life, so its
// agent is replaced at its next idle moment by a fresh one on the new tier, in its worktree, whose brief
// says why (decide.TierRaisedBrief); the lead restarts in place on it once idle (leadtier.go), with its
// conversation. Facts alone decide it, never a model's say.

// raise works out the step phase of run st (plan pl) is raised now, from reds red runs of its gate (the last
// of them last), the loop that holds it running on served (for a part its tier leaves to the session): the
// raise, or false with why not (its steps are used, or no step is above it).
func (t tiering) raise(st state.State, pl plan.Plan, phase string, served plan.Tier, reds int, last *state.Job, now time.Time) (state.TierRaise, string, bool) {
	n := len(state.Raises(st, pl, phase)) // a tier the plan was given since its raises starts the steps again
	from := state.TierFor(st, pl, phase, t.Models)
	if n >= t.Steps {
		return state.TierRaise{}, "its steps are used", false
	}
	to, ok := plan.Raise(from, served, t.Models)
	if !ok {
		return state.TierRaise{}, "no step is above " + from.String(), false
	}
	if from.Model == "" {
		from.Model = served.Model
	}
	if from.Effort == "" {
		from.Effort = served.Effort
	}
	command := ""
	if last != nil {
		command = last.Command
	}
	return state.TierRaise{From: from, To: to, Plan: pl.TierOf(phase), Why: decide.RaiseWhy(reds, command), Reds: reds, At: now}, "", true
}

// tiering is the loop's tier settings, with how it raises a phase.
type tiering struct {
	Models       []string
	Steps, After int
}

func (l *Loop) tiering() tiering {
	return tiering{Models: l.Tiers.Models, Steps: l.Tiers.Steps, After: l.Tiers.After}
}

// escalateTo records the raise of phase in the lead's run s, unless it was raised meanwhile (its last
// raise is no longer since), announces it and logs it in log (the loop's own run). lead says the lead holds
// the phase. It reports whether it recorded one.
func escalateTo(s, log *state.Store, phase string, since time.Time, r state.TierRaise, lead bool) bool {
	raised := false
	s.Update(func(st *state.State) error {
		if !st.LastRaise(phase).Equal(since) {
			return nil // another step took it meanwhile
		}
		state.RaiseTier(st, phase, r)
		st.Run.Announce = append(st.Run.Announce, decide.TierRaised(phase, r.From, r.To, r.Why, lead))
		raised = true
		return nil
	})
	if raised {
		log.Event("tier_raised", map[string]any{"phase": phase, "from": r.From.String(), "to": r.To.String(), "reds": r.Reds, "why": r.Why, "by": map[bool]string{true: "lead", false: "agent"}[lead]})
	}
	return raised
}

// escalateLead raises the phase the lead holds, from its own run's jobs (escalate.go's rule), and returns
// the run's state as it then stands. It runs before the tick delivers anything, so that a red result that
// raises the phase finds the lead idle, and the lead restarts on its raised tier (retierIdle) before it is
// woken for the result. A red whole run counts once its environment is read, which this tick's settle may
// have done after st was read: then the state is read again.
func (l *Loop) escalateLead(v host.View, st state.State) state.State {
	t := l.tiering()
	if t.Steps == 0 || t.After == 0 || st.Mode != state.ModeRunning || !st.InHand() || l.Store.Worker() != "" {
		return st
	}
	phase := st.Current
	since := st.LastRaise(phase)
	reds, last := state.RedRuns(st, phase, since)
	if reds < t.After && unread(st, phase, since) {
		if fresh, err := l.Store.Load(); err == nil {
			st = fresh
			reds, last = state.RedRuns(st, phase, since)
		}
	}
	if reds < t.After {
		return st
	}
	pl, ok := l.tierPlan(v.Now)
	if !ok || pl.TierOf(phase).Zero() {
		return st // a phase its plan names no tier for runs as phases always have: never raised
	}
	r, why, ok := t.raise(st, pl, phase, st.LeadRuns(), reds, last, v.Now)
	if !ok {
		l.ceiling(phase, since, why, reds, l.Store)
		return st
	}
	if escalateTo(l.Store, l.Store, phase, since, r, true) {
		if fresh, err := l.Store.Load(); err == nil {
			return fresh
		}
	}
	return st
}

// unread reports a red whole run of phase's gate in st, ended after since, whose environment the host had
// not read when st was read (state.RedRuns waits for it).
func unread(st state.State, phase string, since time.Time) bool {
	for _, j := range st.Jobs {
		if j.Phase == phase && j.Status == state.JobFailed && j.Ended.After(since) && j.EnvRead.IsZero() && state.WholeRun(j) {
			return true
		}
	}
	return false
}

// ceiling logs, once per phase and step, that a phase gone red again was not raised, and why.
func (l *Loop) ceiling(phase string, since time.Time, why string, reds int, log *state.Store) {
	key := phase + " " + since.String()
	if l.ceiled == nil {
		l.ceiled = map[string]bool{}
	}
	if l.ceiled[key] {
		return
	}
	l.ceiled[key] = true
	log.Event("tier_ceiling", map[string]any{"phase": phase, "reds": reds, "why": why})
}

// escalate raises the phase of phase agent w (its roster entry e) from its sub-run's jobs, and replaces
// an agent at work on a tier its phase no longer runs on (retier): one it was raised from, or one the plan
// file named before the human changed it, whether escalate is on or not.
func (c *Coordinator) escalate(now time.Time, s *state.Store, st state.State, pl plan.Plan, w *worker, e *state.Worker) {
	if e == nil || w.ending != "" || w.agent.in == nil {
		return
	}
	sub, err := w.store.Load()
	if err != nil || sub.Mode != state.ModeRunning {
		return
	}
	// A phase its plan names no tier for runs as phases always have: never raised.
	if t := c.Lead.tiering(); t.Steps > 0 && t.After > 0 && !pl.TierOf(w.phase).Zero() {
		since := st.LastRaise(w.phase)
		if reds, last := state.RedRuns(sub, w.phase, since); reds >= t.After {
			if r, why, ok := t.raise(st, pl, w.phase, sub.Run.Served(), reds, last, now); ok {
				if escalateTo(s, w.store, w.phase, since, r, false) {
					return // its agent is replaced on a later step, with the raise on record
				}
			} else {
				c.Lead.ceiling(w.phase, since, why, reds, w.store)
			}
		}
	}
	c.retier(now, s, st, pl, w, e, sub)
}

// retier replaces the agent of w (its roster entry e, its sub-run sub) at work on a tier its phase no
// longer runs on (state.TierFor): at its next idle moment, or Timing.TierWait after baton first saw the
// change whatever the agent is doing, so that the change comes. A phase's tier changes when baton raises it
// or when the human changes it in the plan file, up or down: the plan sets each phase's tier. The fresh
// agent starts on the new tier in the same worktree (respawn). An agent spawned on no tier (before v0.8.18)
// keeps on, and so does one whose phase's plan names no tier now: nothing would say what to replace it
// with.
func (c *Coordinator) retier(now time.Time, s *state.Store, st state.State, pl plan.Plan, w *worker, e *state.Worker, sub state.State) {
	want := state.TierFor(st, pl, w.phase, c.Lead.Tiers.Models)
	if e.Status != state.WorkerWorking || e.Tier.Zero() || want.Zero() || state.Fits(want, e.Tier) {
		w.retierTo = plan.Tier{}
		return
	}
	if w.retierTo != want {
		w.retierTo, w.retierAt = want, now
	}
	since, by, why := w.retierAt, "plan", decide.AgentTierChanged(want)
	if r := state.LastRaised(st, pl, w.phase); r != nil && r.To == want {
		by, why = "raise", decide.AgentTierRaised(want)
		if r.At.Before(since) {
			since = r.At // its wait counts from the raise
		}
	}
	idle := w.sending == 0 && !sub.Run.TurnOpen
	if !idle && now.Sub(since) < c.Lead.Timing.TierWait {
		return
	}
	c.setEntry(s, w.entry, func(x *state.Worker) {
		if x.Status == state.WorkerWorking {
			x.Status, x.Since, x.Why, x.Resume = state.WorkerSuspended, now, why, ""
		}
	})
	w.retierTo = plan.Tier{}
	w.store.Event("agent_retiered", map[string]any{"agent": w.entry.AgentID, "from": e.Tier.String(), "to": want.String(), "idle": idle, "by": by})
}
