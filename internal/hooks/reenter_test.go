package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// History is what decides whether /baton run compacts before the first phase: a turn that ended, or an
// earlier conversation resumed, is something to compact away; a new conversation, or one /clear started
// over, is not.
func TestTheConversationHasHistoryOnceATurnEndsOrItResumes(t *testing.T) {
	f := newFixture(t, false)
	history := func() bool { return f.state().Run.History }
	steps := []struct {
		event string
		input map[string]any
		want  bool
	}{
		{"SessionStart", map[string]any{"source": "startup"}, false},
		{"UserPromptSubmit", map[string]any{"prompt": "/baton run plan.md"}, false},
		{"Stop", map[string]any{}, true},
		{"SessionStart", map[string]any{"source": "clear"}, false},
		{"StopFailure", map[string]any{"error": "rate_limit"}, true},
		{"SessionStart", map[string]any{"source": "startup"}, false},
		{"SessionStart", map[string]any{"source": "resume"}, true},
	}
	for i, s := range steps {
		f.fire(s.event, s.input)
		if history() != s.want {
			t.Fatalf("step %d (%s %v): history %v, want %v", i, s.event, s.input, history(), s.want)
		}
	}
}

// A run resumed after the conversation grew while it was paused records where the phase stands before
// it does anything else; a stop without that is refused, and after MaxStopBlocks baton compacts anyway.
func TestAResumedRunCheckpointsBeforeItGoesOn(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error { st.CheckpointDue = true; return nil })

	if d := denyReason(f.fire("PreToolUse", map[string]any{"tool_name": "Edit"})); !strings.Contains(d, "First record where P0 stands") {
		t.Fatalf("not held: %q", d)
	}
	if out := f.fire("PreToolUse", bash(`baton checkpoint --notes 'half way'`)); out["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "allow" {
		t.Fatalf("the checkpoint itself was held: %v", out)
	}
	if ctx := additionalContext(f.fire("SessionStart", map[string]any{"source": "resume"})); !strings.Contains(ctx, "First record where P0 stands") {
		t.Errorf("primer: %s", ctx)
	}
	for i := 1; i <= MaxStopBlocks; i++ {
		out := f.fire("Stop", map[string]any{})
		if out["decision"] != "block" || !strings.Contains(out["reason"].(string), "baton checkpoint --notes") {
			t.Fatalf("stop %d: %v", i, out)
		}
	}
	if msg := say(f.fire("Stop", map[string]any{})); !strings.Contains(msg, "compacting anyway, then P0 (First) continues") {
		t.Fatalf("after %d refusals: %q", MaxStopBlocks, msg)
	}
	st := f.state()
	if st.CheckpointDue || st.Run.Compaction.Status != state.CompactQueued || st.Run.Compaction.Reason != "checkpoint" {
		t.Fatalf("state %+v", st)
	}
	if !strings.Contains(f.eventLog(), `"notes":false`) {
		t.Errorf("events: %s", f.eventLog())
	}
}

// A plan attached in a conversation with history waits for the compaction; a session that starts (or
// resumes) in that state is told so.
func TestThePrimerSaysThePhaseWaitsForTheCompaction(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error { *st = state.AttachAtBoundary(*st, pl, f.now); return nil })
	if ctx := additionalContext(f.fire("SessionStart", map[string]any{"source": "resume"})); !strings.Contains(ctx, "P0 has not started: baton compacts the context first") {
		t.Errorf("primer: %s", ctx)
	}
	if d := denyReason(f.fire("PreToolUse", map[string]any{"tool_name": "Edit"})); !strings.Contains(d, "compacts the conversation so far before P0 begins") {
		t.Errorf("hold: %q", d)
	}
}
