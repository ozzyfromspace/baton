package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// agentNameTaken refuses a model's own Agent call named for one of the run's phases, ignoring case
// (plan.Plan.Named): a phase's id is the name of baton's agent on it, the human's handle on that agent,
// and Claude Code gives a name to the last agent that took it. The lead's model and every agent's are held
// to it, a phase agent's by the plan of the run it works for; baton's mod's own spawn (modCall) never comes
// here. A run with no plan attached refuses nothing. It reports whether it refused, and fails open: a
// run or a plan it cannot read refuses nothing.
func (h *handlers) agentNameTaken(c Context) (Result, bool) {
	ti, _ := c.Input["tool_input"].(map[string]any)
	name := str(ti, "name")
	if str(c.Input, "tool_name") != "Agent" || name == "" {
		return Result{}, false
	}
	s, err := h.d.Open(c)
	if err != nil || s == nil {
		return Result{}, false
	}
	run := s
	if s.Worker() != "" {
		if run, err = s.ParentRun(); err != nil {
			return Result{}, false
		}
	}
	st, err := run.Load()
	if err != nil || st.Mode == state.ModeIdle {
		return Result{}, false
	}
	pl, err := run.LoadPlan()
	if err != nil {
		return Result{}, false
	}
	phase := pl.Named(name)
	if phase == "" {
		return Result{}, false
	}
	fields := map[string]any{"name": name, "phase": phase}
	if from := s.Worker(); from != "" {
		fields["from"] = from // a phase agent's call, or one of its own agents'
	}
	run.Event("agent_name_refused", fields)
	return permission("deny", decide.AgentNameIsPhase(name, phase)), true
}
