package hooks

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
