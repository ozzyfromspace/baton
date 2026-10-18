package hooks

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A check command the shell ran counts for the phase in hand, passing or not; anything else, another tool,
// or no phase in hand counts nothing.
func TestCountCheck(t *testing.T) {
	pl := plan.Plan{Phases: []plan.Phase{{ID: "P0", After: []string{}}, {ID: "P1", After: []string{"P0"}}}}
	inHand := func() *state.State {
		st := state.Attach(pl, time.Now(), state.Origin{})
		st.Run = state.Runtime{}
		return &st
	}
	call := func(tool, cmd string) map[string]any {
		return map[string]any{"tool_name": tool, "tool_input": map[string]any{"command": cmd}}
	}
	for _, c := range []struct {
		name string
		st   func() *state.State
		tool string
		cmd  string
		want int
	}{
		{"go test", inHand, "Bash", "go test -run TestA ./internal/x", 1},
		{"a lint in a chain", inHand, "Bash", "cd web && pnpm lint", 1},
		{"in PowerShell", inHand, "PowerShell", "go test ./...", 1},
		{"not a check", inHand, "Bash", "git status", 0},
		{"another tool", inHand, "Read", "go test ./...", 0},
		{"at a boundary", func() *state.State { st := inHand(); st.OweBoundary(time.Now()); return st }, "Bash", "go test ./...", 0},
		{"no plan", func() *state.State { st := state.New(); return &st }, "Bash", "go test ./...", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := c.st()
			countCheck(st, c.tool, call(c.tool, c.cmd))
			if got := st.Checks("P0"); got != c.want {
				t.Errorf("checks = %d, want %d", got, c.want)
			}
		})
	}
}

// PostToolUse counts the lead's check commands for the phase in hand, a failing one too.
func TestPostToolUseCountsChecks(t *testing.T) {
	f := newFixture(t, true)
	f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1", "tool_input": map[string]any{"command": "go test ./internal/x"}})
	f.fire("PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_use_id": "t2", "tool_input": map[string]any{"command": "go vet ./..."}})
	f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t3", "tool_input": map[string]any{"command": "ls"}})
	if got := f.state().Checks("P0"); got != 2 {
		t.Errorf("checks = %d, want 2", got)
	}
}
