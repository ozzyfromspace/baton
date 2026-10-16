package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan baton could not attach as written is turned back before the human sees it, once per text, with
// what keeps it from attaching; fixed, it goes on to the directives turn-back, and the same text again
// goes on as it is.
func TestAPlanThatWouldNotAttachIsTurnedBackBeforeApproval(t *testing.T) {
	broken := "# Broken\n\n## P0 — First\nDo a.\n\n## P1 — Second\nAfter: P7\nDo b.\n"
	for _, c := range []struct {
		name, problem string
		next          string // the plan put again: the same text, or fixed
		nextDeny      string // what the next ExitPlanMode is turned back with
	}{
		{"fixed", `After: names "P7", which is not a phase of this plan`, strings.Replace(broken, "After: P7", "After: P0", 1), "List every directive that competes"},
		{"put again as it is", `After: names "P7", which is not a phase of this plan`, broken, "List every directive that competes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, false)
			d, why, shown := f.exitPlan("/plans/broken.md", broken)
			if d != "deny" || !strings.HasPrefix(why, "[baton] baton could not attach this plan as written") || !strings.Contains(why, c.problem) ||
				!strings.Contains(why, "Then call ExitPlanMode again") {
				t.Fatalf("first call: %q\n%s", d, why)
			}
			if !strings.Contains(shown, "baton: the plan would not attach as written (") || !strings.Contains(shown, "→ Claude fixes it before you see it") {
				t.Errorf("showed %q", shown)
			}
			if !f.logged("plan_unbuilt", map[string]any{"plan": "/plans/broken.md", "problems": 1.0}) {
				t.Errorf("events:\n%s", f.eventLog())
			}
			d, why, _ = f.exitPlan("/plans/broken.md", c.next)
			if d != "deny" || !strings.Contains(why, c.nextDeny) || strings.Contains(why, "could not attach") {
				t.Fatalf("second call: %q\n%s", d, why)
			}
			if d, _, _ = f.exitPlan("/plans/broken.md", c.next); d == "deny" {
				t.Fatal("the plan never reached the human")
			}
			if n := len(f.eventsOf("plan_unbuilt")); n != 1 {
				t.Errorf("turned back %d times", n)
			}
		})
	}
}

// Turned back for a plan that would not attach, a model that stops in plan mode is sent back once to put
// the plan to the human.
func TestAStopAfterAnUnbuiltPlanIsSentBackOnce(t *testing.T) {
	f := newFixture(t, false)
	f.exitPlan("/plans/twice.md", "# Twice\n\n## P0 — A\nx\n\n## P0 — B\ny\n")
	stop := f.fire("Stop", map[string]any{"permission_mode": "plan"})
	if stop["decision"] != "block" || stop["reason"] != decide.ExitPlanAgain() {
		t.Fatalf("stop: %v", stop)
	}
	if stop := f.fire("Stop", map[string]any{"permission_mode": "plan"}); stop["decision"] == "block" {
		t.Fatalf("stop turned back twice: %v", stop)
	}
}

// A plan that attaches, one with no phase headings, and a subagent's plan are not this check's.
func TestOnlyAPlanThatWouldNotAttachIsTurnedBackForIt(t *testing.T) {
	f := newFixture(t, false)
	for _, c := range []struct {
		name string
		in   map[string]any
	}{
		{"attaches", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": approvedDoc, "planFilePath": "/plans/a.md"}}},
		{"no phase headings", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "# Lunch\n\nSandwiches.\n"}}},
		{"a subagent's", map[string]any{"tool_name": "ExitPlanMode", "agent_id": "a1", "tool_input": map[string]any{"plan": "# T\n\n## P0 — A\nAfter: P4\n"}}},
	} {
		out := f.fire("PreToolUse", c.in)
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		if why, _ := hso["permissionDecisionReason"].(string); strings.Contains(why, "could not attach") {
			t.Errorf("%s: turned back: %s", c.name, why)
		}
	}
	if len(f.eventsOf("plan_unbuilt")) != 0 {
		t.Errorf("events:\n%s", f.eventLog())
	}
}

// The check fails open: a panic in it exits 0 and writes nothing.
func TestTheUnbuiltCheckFailsOpen(t *testing.T) {
	f := newFixture(t, false)
	open := func(ctx Context) (*state.Store, error) {
		s, err := OpenRun(f.clock)(ctx)
		if s != nil {
			s.Now = func() time.Time { panic("boom") }
		}
		return s, err
	}
	in := map[string]any{"hook_event_name": "PreToolUse", "session_id": f.sid, "tool_name": "ExitPlanMode",
		"tool_input": map[string]any{"plan": "# T\n\n## P0 — A\nAfter: P4\n", "planFilePath": "/plans/p.md"}}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("PreToolUse", bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
	if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}
