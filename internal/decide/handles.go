package decide

import "fmt"

// A phase's id is the name of baton's agent on it, the human's handle on that agent, so no agent of a
// model's own takes one: Claude Code matches an agent's name ignoring case, and gives a name to the last
// agent that took it (plan's names.go).

// AgentNameIsPhase is why a model's own Agent call named name, which addresses phase's agent (the same id,
// ignoring case), is not run.
func AgentNameIsPhase(name, phase string) string {
	how := ""
	if name != phase {
		how = " (Claude Code matches an agent's name ignoring case)"
	}
	return fmt.Sprintf("[baton] Not run: %q names baton's agent on phase %s of the plan%s. That name is the human's handle on baton's agent, "+
		"and an agent of yours by it would take it over. Give your agent a name that is none of the plan's phase ids, or leave the name out, and run it again.", name, phase, how)
}
