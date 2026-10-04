package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

type fixture struct {
	t     *testing.T
	dir   string
	store *state.Store
	now   time.Time
	inst  string
}

func newFixture(t *testing.T, withPlan bool) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".baton")
	f := &fixture{t: t, dir: dir, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), inst: "inst-1"}
	f.store, _ = state.Open(dir, f.inst, func() time.Time { return f.now })
	if withPlan {
		doc := []byte("# Demo\n\n## P0 — First\nDo a.\n\n## P1 — Second\nDo b.\n")
		docPath := filepath.Join(filepath.Dir(dir), "plan.md")
		os.WriteFile(docPath, doc, 0o644)
		pl, err := plan.Build(docPath, doc, plan.Suggest(doc))
		if err != nil {
			t.Fatal(err)
		}
		f.store.SavePlan(pl)
		f.store.Update(func(st *state.State) error { *st = state.Attach(pl, f.now, ""); return nil })
	}
	f.store.Update(func(st *state.State) error { return state.Claim(st, f.inst, 1, f.now) })
	return f
}

func (f *fixture) env(instance string) func(string) string {
	return func(k string) string {
		return map[string]string{"BATON_HOST": "1", "BATON_DIR": f.dir, "BATON_INSTANCE": instance}[k]
	}
}

// fire runs one hook event through Dispatch with the real handlers and returns its decoded output.
func (f *fixture) fire(event string, input map[string]any) map[string]any {
	return f.fireAs(f.inst, event, input)
}

func (f *fixture) fireAs(instance, event string, input map[string]any) map[string]any {
	f.t.Helper()
	input["hook_event_name"] = event
	raw, _ := json.Marshal(input)
	var out, errb bytes.Buffer
	h := Handlers(Deps{Open: func(env func(string) string) (*state.Store, error) {
		return state.Open(env("BATON_DIR"), env("BATON_INSTANCE"), func() time.Time { return f.now })
	}})
	if code := Dispatch(event, bytes.NewReader(raw), &out, &errb, f.env(instance), func() time.Time { return f.now }, h); code != 0 {
		f.t.Fatalf("%s exited %d: %s", event, code, errb.String())
	}
	if strings.Contains(errb.String(), "failed open") {
		f.t.Fatalf("%s failed open: %s", event, errb.String())
	}
	var res map[string]any
	if out.Len() > 0 {
		json.Unmarshal(out.Bytes(), &res)
	}
	return res
}

func (f *fixture) state() state.State {
	st, err := f.store.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func TestSessionStartPrimer(t *testing.T) {
	f := newFixture(t, false)
	if out := f.fire("SessionStart", map[string]any{"source": "startup"}); !strings.Contains(out["systemMessage"].(string), "no plan attached") {
		t.Fatalf("no plan: %v", out)
	}
	f = newFixture(t, true)
	out := f.fire("SessionStart", map[string]any{"source": "startup", "session_id": "s-1"})
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	for _, want := range []string{"hosted by baton", "Current phase: P0 — First", "baton done <phase>", "baton blocked", "baton waiting", "Never type /compact"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("primer lacks %q:\n%s", want, ctx)
		}
	}
	if !strings.Contains(out["systemMessage"].(string), "0/2 phases done · current P0") {
		t.Errorf("systemMessage: %v", out["systemMessage"])
	}
	if f.state().Run.SessionID != "s-1" {
		t.Error("session id not recorded")
	}
}

func TestNonOwnerSessionIsDormant(t *testing.T) {
	f := newFixture(t, true)
	if out := f.fireAs("someone-else", "SessionStart", map[string]any{"source": "startup"}); out != nil {
		t.Fatalf("non-owner produced output: %v", out)
	}
	f.fireAs("someone-else", "UserPromptSubmit", map[string]any{"prompt": "hi"})
	if f.state().Run.TurnOpen {
		t.Fatal("non-owner changed state")
	}
}

func TestTurnSource(t *testing.T) {
	cases := map[string]string{
		"please continue": "human",
		"[baton] no activity for 10m — report status":               "baton",
		"<task-notification>\nStop hook feedback [baton] compacted": "baton",
		"<task-notification>\nBackground command finished":          "system",
	}
	for prompt, want := range cases {
		if got := TurnSource(prompt); got != want {
			t.Errorf("%q: %s, want %s", prompt, got, want)
		}
	}
}

func TestTurnsDialogsSubagentsAndStops(t *testing.T) {
	f := newFixture(t, true)
	f.fire("UserPromptSubmit", map[string]any{"prompt": "go"})
	if r := f.state().Run; !r.TurnOpen || r.TurnBy != "human" {
		t.Fatalf("turn: %+v", r)
	}
	f.fire("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})
	if d := f.state().Run.Dialog; d == nil || d.Tool != "AskUserQuestion" {
		t.Fatalf("dialog: %+v", d)
	}
	f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})
	if f.state().Run.Dialog == nil {
		t.Fatal("a different tool finishing closed the dialog")
	}
	f.fire("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"})
	if f.state().Run.Dialog != nil {
		t.Fatal("dialog not closed")
	}
	f.fire("SubagentStart", map[string]any{"agent_id": "a"})
	f.fire("SubagentStart", map[string]any{"agent_id": "b"})
	f.fire("SubagentStop", map[string]any{"agent_id": "a"})
	if n := f.state().Run.Subagents; n != 1 {
		t.Fatalf("subagents = %d", n)
	}
	f.fire("Stop", map[string]any{"stop_hook_active": false,
		"background_tasks": []any{
			map[string]any{"id": "1", "type": "shell", "status": "running", "description": "npm test"},
			map[string]any{"id": "2", "type": "monitor", "status": "running"},
		},
		"session_crons": []any{map[string]any{"id": "c"}}})
	r := f.state().Run
	if r.TurnOpen || len(r.Background) != 2 || len(r.BusyBackground()) != 1 || r.Crons != 1 {
		t.Fatalf("after stop: %+v", r)
	}
}

func TestCompactionLifecycle(t *testing.T) {
	f := newFixture(t, true)
	// A compaction nobody asked for (the human typed /compact with nothing owed) is not baton's.
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	if c := f.state().Run.Compaction; c.ByBaton || c.Trigger != "manual" {
		t.Fatalf("unrequested: %+v", c)
	}
	// baton queued one and typed it: the manual PreCompact acknowledges it.
	f.store.Update(func(st *state.State) error {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactTyped, Reason: "boundary"}
		return nil
	})
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	if c := f.state().Run.Compaction; !c.ByBaton || c.Status != state.CompactActive || c.Epoch != 1 {
		t.Fatalf("acknowledged: %+v", c)
	}
	f.fire("PostCompact", map[string]any{"trigger": "manual"})
	if c := f.state().Run.Compaction; c.Status != state.CompactDone {
		t.Fatalf("finished: %+v", c)
	}
	// An auto-compaction while a boundary is owed satisfies it (no second compaction later).
	f.store.Update(func(st *state.State) error { st.BoundaryOwed = true; st.Run.Compaction.Reason = ""; return nil })
	f.fire("PreCompact", map[string]any{"trigger": "auto"})
	if c := f.state().Run.Compaction; !c.ByBaton || c.Reason != "boundary" || c.Epoch != 2 || c.Status != state.CompactActive {
		t.Fatalf("auto while owed: %+v", c)
	}
	events, _ := os.ReadFile(filepath.Join(f.dir, "events.jsonl"))
	if strings.Count(string(events), `"kind":"compact_started"`) != 3 || !strings.Contains(string(events), `"kind":"compact_finished"`) {
		t.Fatalf("events: %s", events)
	}
}

func TestStopFailureAndSessionEnd(t *testing.T) {
	f := newFixture(t, true)
	f.fire("StopFailure", map[string]any{"error": "rate_limit", "error_details": "resets 3pm"})
	f.fire("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	r := f.state().Run
	if r.LastError == nil || r.LastError.Error != "rate_limit" || r.Ended == nil || r.Ended.Reason != "prompt_input_exit" {
		t.Fatalf("%+v", r)
	}
}
