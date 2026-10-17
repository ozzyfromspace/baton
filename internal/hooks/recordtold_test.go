package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The records baton committed reach the lead's model once, with its next tool call or prompt, after any
// compaction owed; never with a subagent's call, and a stop is never refused for them.
func TestTheLeadIsToldOfRecords(t *testing.T) {
	f := newFixture(t, true)
	n := state.RecordNote{Phase: "P0", Path: "docs/RECORD.md", Commit: "aaaaaaa1111", At: f.now}
	f.set(func(st *state.State) {
		st.Records = append(st.Records, n)
		st.BoundaryOwed = true
	})
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Fatalf("told with a compaction owed: %v", out)
	}
	f.set(func(st *state.State) { st.BoundaryOwed = false })
	if out := f.fire("Stop", map[string]any{}); strings.Contains(str(out, "reason"), "the plan's record file") || f.state().Records[0].Told {
		t.Fatalf("a stop refused for a record: %v", out)
	}
	if out := f.fire("PostToolUse", byAgent(bash("ls"), "sub")); feedback(out) != "" {
		t.Fatalf("told in a subagent's call: %v", out)
	}
	want := decide.RecordsTold([]state.RecordNote{n})
	if got := feedback(f.fire("PostToolUse", bash("ls"))); !strings.Contains(got, want) {
		t.Fatalf("feedback %q, want %q", got, want)
	}
	for _, s := range []string{"P0 (aaaaaaa)", "docs/RECORD.md", "HEAD moved", "leave it to baton"} {
		if !strings.Contains(want, s) {
			t.Errorf("the words lack %q: %s", s, want)
		}
	}
	if !f.state().Records[0].Told || !f.logged("record_told", map[string]any{"via": "tool"}) {
		t.Errorf("not recorded as told\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Errorf("told twice: %v", out)
	}
	f.set(func(st *state.State) {
		st.Records = append(st.Records, state.RecordNote{Phase: "P1", Path: "docs/RECORD.md", At: f.now})
	})
	if ctx := additionalContext(f.fire("UserPromptSubmit", map[string]any{"prompt": "go on"})); !strings.Contains(ctx, "the record of P1 to docs/RECORD.md") ||
		!f.logged("record_told", map[string]any{"via": "prompt"}) {
		t.Errorf("at the prompt: %q", ctx)
	}
}
