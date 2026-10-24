package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// agentsHold is the lead's stop line with no phase in hand when phase agents' work alone holds the run
// st, of plan pl (state.HeldByAgents), logged as idle_on_agents with the phases at work in agents and those
// behind them; "" when anything else holds it, which decide.NoPhaseYet says.
func agentsHold(st state.State, d *decision, pl plan.Plan) string {
	h := state.HeldByAgents(st, pl)
	if !h.Holds() {
		return ""
	}
	d.emit("idle_on_agents", map[string]any{"agents": h.AtWork, "behind": h.Held(), "last": h.Last})
	return decide.AgentsHold(h)
}
