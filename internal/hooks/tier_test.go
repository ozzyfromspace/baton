package hooks

import (
	"strings"
	"testing"
)

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

// A plan's first ExitPlanMode asks for its tiers beside its gates: the tier rules with the escalation the
// host passed down, the phases that name none, and those that name a model the human does not allow. Its
// approval line shows its tiers.
func TestThePlansTiersAreAskedFor(t *testing.T) {
	f := newFixture(t, false)
	f.vars = map[string]string{"BATON_MODELS": "sonnet,opus", "BATON_ESCALATE": "1", "BATON_ESCALATE_AFTER": "4"}
	doc := "# Tiered\n\n## P0 — Spike\nAfter: none\nModel: fable\nEffort: max\nTry.\n\n## P1 — Build\nAfter: none\nModel: opus\nEffort: high\nBuild.\n\n## P2 — Docs\nAfter: P0, P1\nDocument.\n"
	d, why, _ := f.exitPlan("/plans/t.md", doc)
	for _, want := range []string{
		"[baton] Give every phase its tier too", "`Model:` (haiku, sonnet or opus)", "after 4 red runs of its own gate", "at most 1 step;",
		"These phases name no model or no effort: P2.", "These phases name a model the human has not allowed (models in baton's config): P0 (it would run on opus).",
	} {
		if d != "deny" || !strings.Contains(why, want) {
			t.Errorf("first call lacks %q: %q", want, why)
		}
	}
	if !f.logged("directives_asked", map[string]any{"plan": "/plans/t.md", "tiers": true, "untiered": 1.0, "not_allowed": 1.0}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if d, _, shown := f.exitPlan("/plans/t.md", doc); d == "deny" || !strings.HasSuffix(shown, " · tiers 1 fable/max, 1 opus/high, 1 the session's") {
		t.Errorf("second call: %q, showed %q", d, shown)
	}
}
