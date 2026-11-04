package loop

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Each phase runs on its tier (state/tier.go), and is checked against what its loop was served, from facts:
// a phase agent's effort from its own hooks, its model from its replies (Agent.served), the lead's both from
// its hooks and its status line. A phase agent is spawned on its tier (Coordinator.issue) and keeps it for
// its life; the lead runs on its launch flags, which a restart in place changes (leadtier.go).

// servedTier records what the agent of w was served, once its loop knows every part of it the agent's
// tier names (or both parts, for a tier left to the session), on its roster entry e: the board shows it,
// and the check is told once (tier_served). A tier Claude Code capped is told to the human, and the phase
// goes on as served, since nothing baton does changes a cap.
func (c *Coordinator) servedTier(s *state.Store, w *worker, e *state.Worker) {
	if e == nil || !e.Served.Zero() || w.agent.in == nil {
		return
	}
	sub, err := w.store.Load()
	if err != nil {
		return
	}
	served, asked := sub.Run.Served(), e.Tier
	if !known(asked, served) {
		return
	}
	c.setEntry(s, w.entry, func(x *state.Worker) { x.Served = served })
	w.entry.Served = served
	fits := state.Fits(asked, served)
	w.store.Event("tier_served", map[string]any{"phase": w.phase, "asked": asked.String(), "served": served.String(), "fits": fits, "by": "agent"})
	if !fits {
		c.announce(s, decide.TierCapped(w.phase, asked, served))
	}
}

// known reports a served tier that says every part asked names, or both parts for a tier asked that names
// neither.
func known(asked, served plan.Tier) bool {
	if asked.Zero() {
		return served.Model != "" && served.Effort != ""
	}
	return (asked.Model == "" || served.Model != "") && (asked.Effort == "" || served.Effort != "")
}
