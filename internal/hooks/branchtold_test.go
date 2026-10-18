package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The run's branch following the lead's checkout reaches the lead's model once, with its next tool call
// or prompt, after any compaction owed; never with a subagent's call.
func TestTheLeadIsToldTheRunFollowsItsBranch(t *testing.T) {
	f := newFixture(t, true)
	note := decide.BranchFollowedNote("main", "fix")
	f.set(func(st *state.State) { st.BranchNote, st.BoundaryOwed = note, true })
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Fatalf("told with a compaction owed: %v", out)
	}
	f.set(func(st *state.State) { st.BoundaryOwed = false })
	if out := f.fire("PostToolUse", byAgent(bash("ls"), "sub")); feedback(out) != "" {
		t.Fatalf("told in a subagent's call: %v", out)
	}
	if got := feedback(f.fire("PostToolUse", bash("ls"))); !strings.Contains(got, note) {
		t.Fatalf("feedback %q, want %q", got, note)
	}
	if f.state().BranchNote != "" || !f.logged("run_branch_told", map[string]any{"via": "tool"}) {
		t.Errorf("not recorded as told\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Errorf("told twice: %v", out)
	}
	f.set(func(st *state.State) { st.BranchNote = note })
	if ctx := additionalContext(f.fire("UserPromptSubmit", map[string]any{"prompt": "go on"})); !strings.Contains(ctx, note) || !f.logged("run_branch_told", map[string]any{"via": "prompt"}) {
		t.Errorf("at the prompt: %q", ctx)
	}
}
