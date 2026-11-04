package hooks

import "testing"

// Every hook says the effort its loop runs on, and SessionStart the model too (spike 43): the lead's run
// records both from the lead's own hooks, and never from a subagent the lead's model started, whose hooks
// come to the lead's run with an effort of their own.
func TestHooksSayWhatTheLoopRunsOn(t *testing.T) {
	f := newFixture(t, true)
	f.fire("SessionStart", map[string]any{"source": "startup", "model": "claude-opus-5-5"})
	if r := f.state().Run; r.Model != "claude-opus-5-5" || r.Effort != "" {
		t.Fatalf("after SessionStart: %q %q", r.Model, r.Effort)
	}
	read := func(extra map[string]any) map[string]any {
		in := map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "a.go"}, "tool_response": map[string]any{}}
		for k, v := range extra {
			in[k] = v
		}
		return in
	}
	f.fire("PostToolUse", read(map[string]any{"effort": map[string]any{"level": "xhigh"}}))
	if r := f.state().Run; r.Model != "claude-opus-5-5" || r.Effort != "xhigh" || r.Served().String() != "opus/xhigh" {
		t.Fatalf("after the lead's hook: %q %q (%s)", r.Model, r.Effort, r.Served())
	}
	f.fire("PostToolUse", read(map[string]any{"effort": map[string]any{"level": "low"}, "agent_id": "a-explore", "agent_type": "Explore"}))
	if r := f.state().Run; r.Effort != "xhigh" {
		t.Errorf("a subagent of the lead's model moved the lead's effort to %q", r.Effort)
	}
	f.fire("PostToolUse", read(nil))
	if r := f.state().Run; r.Effort != "xhigh" {
		t.Errorf("a hook that says no effort cleared it: %q", r.Effort)
	}
	f.fire("SessionStart", map[string]any{"source": "resume"})
	if r := f.state().Run; r.Model != "claude-opus-5-5" {
		t.Errorf("a resume's SessionStart, which names no model, cleared it: %q", r.Model)
	}
}
