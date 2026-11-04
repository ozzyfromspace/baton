package hooks

import (
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// tierSaid is what a hook's input says its loop runs on (state.SawTier): the model, which only SessionStart
// names, at a session's startup, and the effort, which every hook but SessionStart and SubagentStart
// carries, a phase agent's its own (docs/research/tiers.md). Either is "" when the input does not say.
func tierSaid(c Context) (model, effort string) {
	if c.Event == "SessionStart" {
		model = str(c.Input, "model")
	}
	if e, ok := c.Input["effort"].(map[string]any); ok {
		effort = str(e, "level")
	}
	return model, effort
}

// workerTier is the tier phase runs on, and the step it was last raised, if any, as the run a worker's
// sub-run s works for has them (its own keeps neither): what its brief says (brief.Input.Tier). nil when
// that run cannot be read.
func workerTier(s *state.Store, phase string) (*plan.Tier, *state.TierRaise) {
	up, err := s.ParentRun()
	if err != nil {
		return nil, nil
	}
	st, err := up.Load()
	if err != nil {
		return nil, nil
	}
	pl, err := up.LoadPlan()
	if err != nil {
		return nil, nil
	}
	t := state.TierFor(st, pl, phase, st.Run.Models)
	if ps := st.Phases[phase]; ps != nil && len(ps.Raises) > 0 {
		r := ps.Raises[len(ps.Raises)-1]
		return &t, &r
	}
	return &t, nil
}
