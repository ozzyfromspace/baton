package hooks

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// agentCallOf is a PreToolUse's input for a call of the Agent tool with input in.
func agentCallOf(id string, in map[string]any) map[string]any {
	return map[string]any{"tool_name": "Agent", "tool_use_id": id, "tool_input": in}
}

// denied is why f's PreToolUse refused the call in, or "" for a call it let through.
func denied(f *fixture, in map[string]any) string {
	hso, _ := f.fire("PreToolUse", in)["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		return ""
	}
	why, _ := hso["permissionDecisionReason"].(string)
	return why
}

// A phase's id is the human's handle on baton's agent on it: a model's own agent may not take it, the
// lead's or an agent's, by the same id in any case. Other names, and none, go through; baton's mod's own
// spawn goes through; and a refused call holds no worktree back.
func TestAModelsAgentMayNotTakeAPhasesName(t *testing.T) {
	w, lead, _ := newWorker(t)
	var ran []string // the calls let through
	for i, c := range []struct {
		name  string
		f     *fixture
		agent string // the name the call gives its agent
		phase string // the phase it is refused for, "" for a call let through
		from  string // the phase agent whose call it is, logged
	}{
		{"the lead's, a phase's id", lead, "P1", "P1", ""},
		{"the lead's, a phase's id in another case", lead, "p2", "P2", ""},
		{"the lead's, a done phase's id", lead, "P0", "P0", ""},
		{"a phase agent's, another phase's id", w, "P1", "P1", "P2"},
		{"a phase agent's, its own phase's id", w, "p2", "P2", "P2"},
		{"the lead's, another name", lead, "helper", "", ""},
		{"the lead's, a name with a phase's id in it", lead, "P1-helper", "", ""},
		{"a phase agent's, another name", w, "scout", "", ""},
	} {
		id := fmt.Sprintf("toolu_%d", i)
		if c.phase == "" {
			ran = append(ran, id)
		}
		t.Run(c.name, func(t *testing.T) {
			why := denied(c.f, agentCallOf(id, map[string]any{"subagent_type": "Explore", "name": c.agent, "prompt": "Look around."}))
			if c.phase == "" {
				if why != "" {
					t.Errorf("refused: %q", why)
				}
				return
			}
			if want := decide.AgentNameIsPhase(c.agent, c.phase); why != want {
				t.Errorf("said %q, want %q", why, want)
			}
			fields := map[string]any{"name": c.agent, "phase": c.phase}
			if c.from != "" {
				fields["from"] = c.from
			}
			if !lead.logged("agent_name_refused", fields) {
				t.Errorf("not logged %v:\n%s", fields, lead.eventLog())
			}
		})
	}
	if why := denied(lead, agentCallOf("toolu_8", map[string]any{"subagent_type": "Explore", "prompt": "Look around."})); why != "" {
		t.Errorf("an agent with no name: %q", why)
	}
	ran = append(ran, "toolu_8")
	var called []string
	for _, a := range lead.state().Run.AgentCalls {
		called = append(called, a.ToolUse)
	}
	if slices.Sort(called); !slices.Equal(called, slices.Sorted(slices.Values(ran))) {
		t.Errorf("the calls recorded are %v, the ones let through %v: a refused call holds no worktree back", called, ran)
	}
	spawn := map[string]any{"subagent_type": state.PhaseAgent, "name": "P1", "description": "P1 — Second", "prompt": "Do P1."}
	if out := lead.fire("PreToolUse", agentCallOf("toolu_plugin_9", spawn)); len(out) != 0 {
		t.Errorf("baton's mod's spawn: %v", out)
	}
	if n := len(lead.eventsOf("agent_name_refused")); n != 5 {
		t.Errorf("%d refusals logged:\n%s", n, lead.eventLog())
	}
}

// With no plan attached, no name is a phase's.
func TestAnAgentsNameIsFreeWithNoPlan(t *testing.T) {
	f := newFixture(t, false)
	if why := denied(f, agentCallOf("toolu_1", map[string]any{"subagent_type": "Explore", "name": "P0", "prompt": "Look around."})); why != "" {
		t.Errorf("refused: %q", why)
	}
}

// Refusing a phase's name fails open: a panic in it exits 0 and writes nothing.
func TestRefusingAPhasesNameFailsOpen(t *testing.T) {
	call := func() map[string]any {
		return agentCallOf("toolu_1", map[string]any{"subagent_type": "Explore", "name": "P0", "prompt": "Look around."})
	}
	failsOpen(t, []failCase{
		{"the lead's call", "PreToolUse", func(string) map[string]any { return call() }, nil},
		{"a phase agent's call", "PreToolUse", func(tree string) map[string]any { return phaseAgent(call(), tree) }, nil},
	})
}
