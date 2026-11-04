package loop

import (
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The lead runs on its launch flags (spike 43): a phase's tier reaches it only by a restart in place, in
// the same conversation, with --model and --effort, and a restart ends every phase agent of its claude.
// So, while agents can take them, the lead passes over the phases whose tier it does not run on
// (state.LeadFits); it restarts on the tier of a phase it holds at that phase's boundary (the compaction
// baton is about to type, which the restarted session types instead) or, mid-phase (a phase raised, or one
// started with no boundary), once it is idle; and once no plan runs, it restarts back on the human's own
// tier, which a resume with no flags would not give back. Each restart waits for a point nothing would lose
// (restartable: no agent at work, no turn, no dialog, no draft), and new agents are held while one is due
// (retierHolds), so that it comes. The host carries the tier to the restarted process in TierEnv, and the
// human's own arguments stay as they were.

// TierEnv carries the tier the lead restarts on, as "model/effort" (either part may be empty), to the host
// that restarts it, which launches claude with it (cli's withTier).
const TierEnv = "BATON_TIER"

// EncodeTier is t as TierEnv carries it, and DecodeTier reads it back.
func EncodeTier(model, effort string) string { return model + "/" + effort }

// DecodeTier reads a TierEnv value: its model (as written: a family or a full id) and its effort.
func DecodeTier(v string) (model, effort string) {
	model, effort, _ = strings.Cut(strings.TrimSpace(v), "/")
	return strings.TrimSpace(model), strings.TrimSpace(effort)
}

// tierPlanEvery is how often the lead's tier reads its plan again: the tick is 250ms, and a phase's tier
// changes only with the plan or a raise.
const tierPlanEvery = 2 * time.Second

// tierPlan is the run's plan as the lead's tier reads it, read again every tierPlanEvery.
func (l *Loop) tierPlan(now time.Time) (plan.Plan, bool) {
	if l.tierAt.IsZero() || now.Sub(l.tierAt) >= tierPlanEvery || l.tierRun != l.Store.Dir {
		l.tierAt, l.tierRun = now, l.Store.Dir
		p, err := l.Store.LoadPlan()
		l.tierPl, l.tierOK = p, err == nil
	}
	return l.tierPl, l.tierOK
}

// recordTier records on the run what the host launched the lead on and the families it allows (the lead
// passes over the phases it does not run on, state.LeadFits), and, while the host runs on the human's own
// flags, what the lead runs on as the human's tier (restored once no plan runs). It returns the run's state
// as it then stands.
func (l *Loop) recordTier(st state.State) state.State {
	human := !l.Overridden && st.Run.Model != "" && st.Run.Effort != "" && (st.Run.HumanModel != st.Run.Model || st.Run.HumanEffort != st.Run.Effort)
	if st.Run.Launched == l.Launch && slices.Equal(st.Run.Models, l.Tiers.Models) && !human {
		return st
	}
	up, err := l.Store.Update(func(st *state.State) error {
		st.Run.Launched, st.Run.Models = l.Launch, slices.Clone(l.Tiers.Models)
		if human {
			st.Run.HumanModel, st.Run.HumanEffort = st.Run.Model, st.Run.Effort
		}
		return nil
	})
	if err != nil {
		return st
	}
	return up
}

// leadWant is the tier the lead is to run on while the plan runs: the tier of the phase it holds, or is
// about to start at a boundary (state.TierFor). Zero for none.
func (l *Loop) leadWant(v host.View, st state.State) plan.Tier {
	if st.Mode != state.ModeRunning || st.Current == "" || !st.InHand() && !st.BoundaryOwed {
		return plan.Tier{}
	}
	pl, ok := l.tierPlan(v.Now)
	if !ok {
		return plan.Tier{}
	}
	return state.TierFor(st, pl, st.Current, l.Tiers.Models)
}

// retierDue reports the tier the lead is to restart on, when a part the tier it is to run on names differs
// from the one it runs on (state.LeadRuns), as far as is known, and the host can restart it in place. A part
// the tier leaves to the session keeps what the lead runs on.
func (l *Loop) retierDue(v host.View, st state.State) (plan.Tier, bool) {
	want := l.leadWant(v, st)
	if want.Zero() || !l.Retiers {
		return want, false
	}
	runs := st.LeadRuns()
	due := want.Model != "" && runs.Model != "" && want.Model != runs.Model || want.Effort != "" && runs.Effort != "" && want.Effort != runs.Effort
	if want.Model == "" {
		want.Model = runs.Model
	}
	if want.Effort == "" {
		want.Effort = runs.Effort
	}
	return want, due
}

// retierAtBoundary restarts the lead on the tier of the phase it is about to start, at its boundary
// compaction (CompactQueued), which the restarted session types. Until a restart nothing would lose can
// come (restartable), the compaction is not typed, so the phase starts on its own tier; it reports that it
// restarted or holds the compaction.
func (l *Loop) retierAtBoundary(v host.View, st state.State, in host.Injector) bool {
	if !st.BoundaryOwed {
		return false
	}
	want, due := l.retierDue(v, st)
	if !due {
		return false
	}
	if l.restartable(v, st, l.why) && !v.Draft {
		l.retierOn(v, st, in, want, st.Current, "its boundary", false)
		return true
	}
	l.tierWaits(v, st, want)
	return true
}

// retierIdle restarts the lead on the tier of the phase it holds, mid-phase, once it is idle with nothing a
// restart would end (restartable): a phase baton raised, or one that started with no boundary. Its
// conversation goes on, and it is told to take the phase up again on its new tier (state.MessageTier). It
// reports whether it restarted.
func (l *Loop) retierIdle(v host.View, st state.State, in host.Injector) bool {
	switch st.Run.Compaction.Status {
	case "", state.CompactDone, state.CompactCancelled:
	default:
		return false // a compaction under way, or one that failed, which its own retry has
	}
	// No wait for a quiet screen: while a gate runs, baton's own status line redraws its time every
	// second. The turn's end, the gate (restartable) and the draft say all a restart needs.
	if st.BoundaryOwed || !st.InHand() || st.Run.TurnOpen || v.Draft {
		return false
	}
	want, due := l.retierDue(v, st)
	if !due || !l.restartable(v, st, l.why) {
		return false
	}
	l.retierOn(v, st, in, want, st.Current, "mid-phase", true)
	return true
}

// restoreDue reports the human's own tier, when no plan runs (complete, or none attached) and the lead runs
// on one baton gave it: the restart it takes waits for the session to be idle (restartable).
func (l *Loop) restoreDue(st state.State) (plan.Tier, bool) {
	if !l.Retiers || !l.Overridden || st.Mode != state.ModeComplete && st.Mode != state.ModeIdle || st.Run.HumanModel == "" {
		return plan.Tier{}, false
	}
	human := plan.Tier{Model: st.Run.HumanModel, Effort: st.Run.HumanEffort}
	runs := st.LeadRuns()
	return human, plan.Family(human.Model) != runs.Model || human.Effort != "" && human.Effort != runs.Effort
}

// restoreIdle restarts the lead back on the human's own tier once no plan runs and it is idle
// (restoreDue). It reports whether it restarted.
func (l *Loop) restoreIdle(v host.View, st state.State, in host.Injector) bool {
	human, due := l.restoreDue(st)
	if !due || v.Draft || !l.restartable(v, st, l.gate(v, st)) {
		return false
	}
	l.retierOn(v, st, in, human, "", "no plan runs", false)
	return true
}

// retierHolds reports a restart of the lead on its phase's tier that is due while it holds that phase or is
// about to start it: no new phase agent starts until it has come (Coordinator.retierFor), or the agents at
// work would keep it from ever coming.
func (l *Loop) retierHolds(v host.View, st state.State) (plan.Tier, bool) {
	if l.quit {
		return plan.Tier{}, false
	}
	return l.retierDue(v, st)
}

// tierWaits logs, once per phase and tier, that the lead's boundary waits for a restart on the phase's
// tier.
func (l *Loop) tierWaits(v host.View, st state.State, want plan.Tier) {
	key := st.Current + " " + want.String()
	if l.tierWaited == key {
		return
	}
	l.tierWaited = key
	fields := map[string]any{"phase": st.Current, "tier": want.String(), "runs": st.LeadRuns().String()}
	if l.why != "" {
		fields["why"] = l.why
	}
	if n := len(st.PhaseAgents()); n > 0 {
		fields["agents"] = n
	}
	l.Store.Event("tier_waits", fields)
}

// retierOn ends claude so that the host restarts the session in place on tier t, for phase ("" for the
// human's own tier), resuming the same conversation (TierEnv). mid says the lead is mid-phase, and is told
// to take its phase up again once restarted.
func (l *Loop) retierOn(v host.View, st state.State, in host.Injector, t plan.Tier, phase, why string, mid bool) {
	runs := st.LeadRuns()
	l.quit = true
	l.restart = &restart{to: l.Version, session: st.Run.SessionID, tier: EncodeTier(t.Model, t.Effort)}
	shown := plan.Tier{Model: plan.Family(t.Model), Effort: t.Effort}
	if shown.Model == "" {
		shown.Model = t.Model
	}
	l.update(func(st *state.State) {
		tier := shown
		st.Run.Restart = &state.Restart{From: l.Version, To: l.Version, At: v.Now, Tier: &tier, Phase: phase}
		if mid {
			why := ""
			if ps := st.Phases[phase]; ps != nil && len(ps.Raises) > 0 {
				why = ps.Raises[len(ps.Raises)-1].Why
			}
			state.Tell(st, state.Message{Kind: state.MessageTier, Phase: phase, Text: decide.TierRestartedMail(phase, shown, why)}, v.Now)
		}
		st.Run.Announce = append(st.Run.Announce, decide.TierRestarting(phase, shown))
	})
	l.Store.Event("tier_restart", map[string]any{"phase": phase, "from": runs.String(), "to": shown.String(), "why": why, "session": st.Run.SessionID})
	l.logf("loop: restarting the session on %s (%s)", shown, why)
	if err := in.Quit(); err != nil {
		l.logf("loop: quitting: %v", err)
	}
}

// Retier reports that the loop ended claude to restart the session in place on another tier, and that tier
// as TierEnv carries it.
func (l *Loop) Retier() (string, bool) {
	if l.restart == nil || l.restart.tier == "" {
		return "", false
	}
	return l.restart.tier, true
}
