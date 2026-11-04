package hooks

import (
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
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

// tierCheck is the tiers' part of baton's one turn-back at a plan's first ExitPlanMode (decide.TierCheck),
// asked of every plan beside the gate rules, with the settings the host passed down (config.TiersFromEnv).
// It adds to fields, which directives_asked logs, `tiers: true` and, for a plan that builds, how many of its
// phases name no tier (`untiered`) and how many a model the human does not allow (`not_allowed`).
func tierCheck(c Context, pl plan.Plan, built bool, fields map[string]any) string {
	t := config.TiersFromEnv(c.Env)
	fields["tiers"] = true
	var untiered []string
	disallowed := map[string]string{}
	if built {
		untiered = pl.Untiered()
		for _, ph := range pl.Phases {
			if ph.Model != "" && !t.Allows(ph.Model) {
				disallowed[ph.ID] = state.AllowedModel(ph.Model, t.Models)
			}
		}
		fields["untiered"], fields["not_allowed"] = len(untiered), len(disallowed)
	}
	return decide.TierCheck(untiered, disallowed, t.Allows("fable"), t.Steps, t.After)
}
