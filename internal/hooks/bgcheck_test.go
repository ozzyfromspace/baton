package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// shellCall is the PreToolUse of a Bash or PowerShell call of command, run in the background or not.
func shellCall(tool, command string, background bool) map[string]any {
	ti := map[string]any{"command": command, "description": "run it"}
	if background {
		ti["run_in_background"] = true
	}
	return map[string]any{"tool_name": tool, "tool_use_id": "toolu_1", "tool_input": ti}
}

// A check (lane.IsCheck) the model starts as a background shell task while its plan runs is refused once
// and pointed to a job of baton's (this project has no git, so baton await; decide's test has the words
// with git). A command that is no check, a check in the foreground, baton's own command, a subagent's call
// and a call while baton is paused are left alone.
func TestABackgroundCheckIsQuestioned(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     map[string]any
		paused bool
		asked  bool
	}{
		{name: "go test in the background", in: shellCall("Bash", "go test ./...", true), asked: true},
		{name: "a make target named for a check", in: shellCall("Bash", "make e2e BATON_E2E=1", true), asked: true},
		{name: "a package script", in: shellCall("Bash", "pnpm --filter web test:e2e", true), asked: true},
		{name: "a check after cd, piped", in: shellCall("Bash", "cd web && npx vitest run 2>&1 | tail -50", true), asked: true},
		{name: "a check on a later line", in: shellCall("Bash", "cd web\npnpm lint", true), asked: true},
		{name: "a check after a here-document", in: shellCall("Bash", "python3 - <<'EOF'\nimport pathlib\n# the lead's fix\nEOF\ncd backend && pytest tests/", true), asked: true},
		{name: "a long check", in: shellCall("Bash", "go test -run '"+strings.Repeat("TestA|", 500)+"TestB' ./...", true), asked: true},
		{name: "a check's words in a here-document only", in: shellCall("Bash", "cat > notes.md <<'EOF'\ngo test ./... runs first\nEOF", true)},
		{name: "a PowerShell check", in: shellCall("PowerShell", "go test ./...", true), asked: true},
		{name: "a dev server", in: shellCall("Bash", "pnpm dev", true)},
		{name: "a build", in: shellCall("Bash", "go build ./...", true)},
		{name: "a log followed", in: shellCall("Bash", "tail -f server.log", true)},
		{name: "a wait", in: shellCall("Bash", "sleep 60", true)},
		{name: "a script of the project's", in: shellCall("Bash", "bash scripts/qa.sh", true)},
		{name: "a check in the foreground", in: shellCall("Bash", "go test -run TestA ./internal/state", false)},
		{name: "baton's own command", in: shellCall("Bash", "baton verify --notes x -- go test ./...", true)},
		{name: "baton's own command, from PowerShell", in: shellCall("PowerShell", "baton verify --notes x -- go test ./...", true)},
		{name: "a subagent's call", in: byAgent(shellCall("Bash", "go test ./...", true), "sub-1")},
		{name: "while baton is paused", in: shellCall("Bash", "go test ./...", true), paused: true},
		{name: "a Monitor", in: map[string]any{"tool_name": "Monitor", "tool_input": map[string]any{"command": "go test ./...", "description": "watch"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			if c.paused {
				f.set(func(st *state.State) { st.Mode = state.ModePaused })
			}
			out := f.fire("PreToolUse", c.in)
			why := deniedIn(out)
			if !c.asked {
				if why != "" || len(f.eventsOf("background_questioned")) != 0 || len(f.state().Run.BackgroundQuestioned) != 0 {
					t.Fatalf("questioned: %q, events %s", why, f.eventLog())
				}
				return
			}
			ti := c.in["tool_input"].(map[string]any)
			cmd := clipWhat(ti["command"].(string)) // what the model and the human read, on one line
			for _, w := range []string{"[baton] Not run yet: this starts a check (" + cmd + ") as a background task of your own",
				"A long check belongs in baton: ", "make the same call again: it goes through."} {
				if !strings.Contains(why, w) {
					t.Errorf("the refusal lacks %q:\n%s", w, why)
				}
			}
			if s := say(out); !strings.HasPrefix(s, "baton: pointed a check started in the background ("+cmd+") to baton ") {
				t.Errorf("announced %q", s)
			}
			if !f.logged("background_questioned", map[string]any{"phase": "P0", "tool": c.in["tool_name"], "command": ti["command"]}) { // whole, on its lines
				t.Errorf("events: %s", f.eventLog())
			}
		})
	}
}

// deniedIn is the reason a PreToolUse's output denies its call with, or "" if it does not.
func deniedIn(out map[string]any) string {
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		return ""
	}
	why, _ := hso["permissionDecisionReason"].(string)
	return why
}

// The question is asked once a command: the same call again, however it is spaced, goes through, and
// another check is asked about in its turn. A call a hold of the run's refuses anyway is left to the hold,
// and asked about once the hold has lifted.
func TestABackgroundCheckIsQuestionedOnceThenRuns(t *testing.T) {
	f := newFixture(t, true)
	if why := denied(f, shellCall("Bash", "go test ./...", true)); why == "" {
		t.Fatal("not questioned")
	}
	if why := denied(f, shellCall("Bash", "go  test\t./...", true)); why != "" {
		t.Fatalf("questioned twice: %q", why)
	}
	if n := len(f.eventsOf("background_questioned")); n != 1 {
		t.Fatalf("logged %d times: %s", n, f.eventLog())
	}
	if why := denied(f, shellCall("Bash", "make lint", true)); why == "" {
		t.Fatal("another check not questioned")
	}
	if why := denied(f, shellCall("Bash", "make lint", true)); why != "" {
		t.Fatalf("questioned twice: %q", why)
	}

	f.set(func(st *state.State) { st.CheckpointOwed = true })
	why := denied(f, shellCall("Bash", "pnpm test", true))
	if why == "" || strings.Contains(why, "background task") || len(f.eventsOf("background_questioned")) != 2 {
		t.Fatalf("under a hold: %q, events %s", why, f.eventLog())
	}
	f.set(func(st *state.State) { st.CheckpointOwed = false })
	if why := denied(f, shellCall("Bash", "pnpm test", true)); !strings.Contains(why, "background task") {
		t.Fatalf("after the hold: %q", why)
	}
}

// A phase agent's background check is asked about in its sub-run, with the job's command as words after
// --; the lead's run hears nothing of it.
func TestAPhaseAgentsBackgroundCheckIsQuestioned(t *testing.T) {
	w, lead, _ := newWorker(t)
	out := w.fire("PreToolUse", shellCall("Bash", "go test ./...", true))
	if why := deniedIn(out); !strings.Contains(why, "`baton verify --notes \"<where the phase stands>\" -- <command>`") {
		t.Fatalf("the agent's refusal: %q", why)
	}
	if !w.logged("background_questioned", map[string]any{"phase": "P2", "command": "go test ./..."}) || len(lead.eventsOf("background_questioned")) != 0 {
		t.Fatalf("events: agent %s, lead %s", w.eventLog(), lead.eventLog())
	}
	if why := deniedIn(w.fire("PreToolUse", shellCall("Bash", "go test ./...", true))); why != "" {
		t.Fatalf("questioned twice: %q", why)
	}
}

// Questioning a background check fails open: a panic in it exits 0 and writes nothing.
func TestQuestioningABackgroundCheckFailsOpen(t *testing.T) {
	failsOpen(t, []failCase{
		{"the lead's background check", "PreToolUse", func(string) map[string]any { return shellCall("Bash", "go test ./...", true) }, nil},
		{"a phase agent's background check", "PreToolUse", func(tree string) map[string]any { return phaseAgent(shellCall("Bash", "go test ./...", true), tree) }, nil},
	})
}
