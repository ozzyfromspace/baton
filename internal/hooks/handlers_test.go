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
	dir   string       // the project's .baton directory
	store *state.Store // the session's run
	now   time.Time
	inst  string
	sid   string            // the session every hook is for, unless its input names another
	vars  map[string]string // extra environment for the hooks
}

func newFixture(t *testing.T, withPlan bool) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".baton")
	f := &fixture{t: t, dir: dir, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), inst: "inst-1", sid: "sess-1"}
	p, err := state.OpenProject(dir, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if f.store, err = p.Bind(state.Binding{Session: f.sid, Instance: f.inst}); err != nil {
		t.Fatal(err)
	}
	if withPlan {
		doc := []byte("# Demo\n\n## P0 — First\nDo a.\n\n## P1 — Second\nDo b.\n")
		docPath := filepath.Join(filepath.Dir(dir), "plan.md")
		os.WriteFile(docPath, doc, 0o644)
		pl, err := plan.Build(docPath, doc, plan.Suggest(doc))
		if err != nil {
			t.Fatal(err)
		}
		f.store.SavePlan(pl)
		f.store.Update(func(st *state.State) error { *st = state.Reattach(*st, pl, f.now, state.Origin{}); return nil })
	}
	return f
}

func (f *fixture) clock() time.Time { return f.now }

// events is the session's run's event log.
func (f *fixture) eventLog() string {
	b, _ := os.ReadFile(filepath.Join(f.store.Dir, "events.jsonl"))
	return string(b)
}

func (f *fixture) env(instance string) func(string) string {
	return func(k string) string {
		if v, ok := f.vars[k]; ok {
			return v
		}
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
	if _, ok := input["session_id"]; !ok {
		input["session_id"] = f.sid
	}
	raw, _ := json.Marshal(input)
	var out, errb bytes.Buffer
	h := Handlers(Deps{Open: OpenRun(f.clock)})
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
	out := f.fire("SessionStart", map[string]any{"source": "startup"})
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	for _, want := range []string{"hosted by baton", "Current phase: P0 — First", "baton done <phase>", "baton note", "baton propose", "baton blocked \"<why>\" --tried", "baton waiting", "Never type /compact"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("primer lacks %q:\n%s", want, ctx)
		}
	}
	if !strings.Contains(out["systemMessage"].(string), "0/2 phases done · current P0") {
		t.Errorf("systemMessage: %v", out["systemMessage"])
	}
	if f.state().Run.SessionID != f.sid {
		t.Error("session id not recorded")
	}
	if strings.Contains(ctx, "Decisions made without the human") {
		t.Errorf("a record with nothing in it:\n%s", ctx)
	}

	// After a /clear the model has none of the run in context: the primer brings the record back. The
	// cleared conversation has a new session id, and keeps its run.
	f.store.Update(func(st *state.State) error {
		_, err := state.AddNote(st, "committed P0 unsigned", "re-sign it", f.now)
		return err
	})
	out = f.fire("SessionStart", map[string]any{"source": "clear", "session_id": "sess-after-clear"})
	if f.state().Run.SessionID != "sess-after-clear" {
		t.Errorf("the run did not follow the cleared session: %q", f.state().Run.SessionID)
	}
	ctx = out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, ".)\n\nDecisions made without the human so far (if they ask, each has its undo):\n\n- d1 (P0, ") ||
		!strings.HasSuffix(ctx, "noted: committed P0 unsigned. Undo: re-sign it") {
		t.Errorf("primer after /clear:\n%s", ctx)
	}
}

func TestNonOwnerSessionIsDormant(t *testing.T) {
	f := newFixture(t, true)
	out := f.fireAs("someone-else", "SessionStart", map[string]any{"source": "startup"})
	if msg, _ := out["systemMessage"].(string); !strings.Contains(msg, "another baton terminal is running this conversation's plan") || out["hookSpecificOutput"] != nil {
		t.Fatalf("non-owner said %v", out)
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
	f.fire("PermissionRequest", ask("Which?"))
	if d, ok := f.state().Run.Dialogs.Only(); !ok || d.Tool != "AskUserQuestion" {
		t.Fatalf("dialog: %+v", f.state().Run.Dialogs)
	}
	f.fire("PostToolUse", bash("ls"))
	if !f.state().Run.Dialogs.AnyOpen() {
		t.Fatal("a different tool finishing closed the dialog")
	}
	f.fire("PostToolUse", answer(ask("Which?"), "A"))
	if f.state().Run.Dialogs.AnyOpen() {
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
	// An automatic PreCompact while a boundary is owed is only Claude Code precomputing a summary: it
	// must not pass for the owed compaction, or baton would wait for one that is not happening.
	f.store.Update(func(st *state.State) error { st.BoundaryOwed = true; st.Run.Compaction.Reason = ""; return nil })
	f.fire("PreCompact", map[string]any{"trigger": "auto"})
	if c := f.state().Run.Compaction; c.Epoch != 1 || c.Status != state.CompactDone {
		t.Fatalf("auto precompute taken for a compaction: %+v", c)
	}
	// The Stop then queues the owed compaction as usual.
	f.fire("Stop", map[string]any{})
	if c := f.state().Run.Compaction; c.Status != state.CompactQueued || c.Epoch != 2 || c.Reason != "boundary" {
		t.Fatalf("not queued after the precompute: %+v", c)
	}
	events, _ := os.ReadFile(filepath.Join(f.store.Dir, "events.jsonl"))
	if strings.Count(string(events), `"kind":"compact_started"`) != 2 || !strings.Contains(string(events), `"kind":"compact_finished"`) ||
		!strings.Contains(string(events), `"kind":"precompact_auto"`) {
		t.Fatalf("events: %s", events)
	}
}

// When Claude Code really compacts on its own while a boundary is owed (mid-turn, after `baton done`),
// that compaction counts: the next phase starts from its brief, and nothing is compacted again.
func TestAutoCompactionSatisfiesAnOwedBoundary(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, false); return err })
	f.fire("PreCompact", map[string]any{"trigger": "auto"})
	ctx := additionalContext(f.fire("SessionStart", map[string]any{"source": "compact"}))
	if !strings.Contains(ctx, "Now: P1 — Second. Begin it now.") {
		t.Fatalf("brief:\n%s", ctx)
	}
	f.fire("PostCompact", map[string]any{"trigger": "auto"})
	st := f.state()
	if st.BoundaryOwed || st.Phases["P1"].Status != state.PhaseActive || st.Run.Compaction.Status != state.CompactDone || !st.Run.Compaction.ByBaton {
		t.Fatalf("boundary not satisfied: %+v", st)
	}
	if out := f.fire("Stop", map[string]any{}); out["decision"] != "block" || f.state().Run.Compaction.Status == state.CompactQueued {
		t.Fatalf("compacted again: %v %+v", out, f.state().Run.Compaction)
	}
}

// A mid-phase auto-compaction after an earlier baton compaction must not look like baton's own: no
// rewake, and no "did not resume" nudge when the turn later ends with a wait.
func TestAutoCompactionAfterABatonOneIsNotBatons(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		st.Run.Compaction = state.Compaction{Epoch: 3, Status: state.CompactDone, Reason: "checkpoint", ByBaton: true, Rewoken: 3}
		return nil
	})
	f.fire("PreCompact", map[string]any{"trigger": "auto"})
	if ctx := additionalContext(f.fire("SessionStart", map[string]any{"source": "compact"})); !strings.Contains(ctx, "compacted the context automatically") {
		t.Fatalf("brief:\n%s", ctx)
	}
	f.fire("PostCompact", map[string]any{"trigger": "auto"})
	if c := f.state().Run.Compaction; c.ByBaton || c.Trigger != "auto" {
		t.Fatalf("auto compaction taken for baton's: %+v", c)
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

// setContext records a status line reading from a 1M-token model.
func (f *fixture) setContext(tokens int) {
	f.t.Helper()
	f.store.Update(func(st *state.State) error {
		st.Run.Context = &state.ContextUse{Tokens: tokens, WindowSize: 1_000_000, UsedPct: float64(tokens) / 10_000}
		return nil
	})
}

func additionalContext(out map[string]any) string {
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	s, _ := hso["additionalContext"].(string)
	return s
}

// With the default 810k cap on a 1M model: checkpoint at 486k, warning at 729k, re-arm 81k below each.
var defaultCap = map[string]string{"BATON_COMPACT_CAP": "810000"}

func TestContextNudgeOncePerCompaction(t *testing.T) {
	f := newFixture(t, true)
	f.vars = defaultCap
	f.setContext(500_000)
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "agent_id": "sub-1"}); out != nil {
		t.Fatalf("nudged a subagent: %v", out)
	}
	out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})
	if out["systemMessage"] != "baton: context at 500k of 810k → checkpoint at the next safe point" {
		t.Fatalf("not announced: %v", out["systemMessage"])
	}
	ctx := additionalContext(out)
	if !strings.Contains(ctx, "Checkpoint due: the context holds 500k tokens, 62% of its 810k limit") || !strings.Contains(ctx, "baton checkpoint --notes") {
		t.Fatalf("nudge: %q", ctx)
	}
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil {
		t.Fatalf("nudged twice: %v", out)
	}
	// Another tenth of the limit later, the request is repeated.
	f.setContext(590_000)
	if !strings.Contains(additionalContext(f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})), "the context holds 590k tokens") {
		t.Fatal("the nudge was not repeated as the context grew")
	}
	f.setContext(500_000)
	// A compaction that leaves the context above the threshold must not start a loop of checkpoints.
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	f.setContext(450_000)
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil {
		t.Fatalf("nudged again without the context dropping: %v", out)
	}
	// Well below the threshold, the nudge re-arms (but does not fire); past it again, it fires once more.
	f.setContext(300_000)
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil || f.state().Run.ContextNudged {
		t.Fatalf("below the threshold: %v nudged=%v", out, f.state().Run.ContextNudged)
	}
	f.setContext(490_000)
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out == nil {
		t.Fatal("the re-armed nudge did not fire")
	}
}

func TestContextWarningAsksTheHumanOnce(t *testing.T) {
	f := newFixture(t, true)
	f.vars = defaultCap
	f.setContext(730_000)
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash", "agent_id": "sub-1"}); out != nil {
		t.Fatalf("warned from a subagent: %v", out)
	}
	out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})
	if out["systemMessage"] != "baton: context at 730k of 810k → asking you whether to checkpoint" {
		t.Fatalf("not announced: %v", out["systemMessage"])
	}
	ctx := additionalContext(out)
	for _, want := range []string{"AskUserQuestion", "the context holds 730k tokens, past the 729k warning line", "on its own at about 777k",
		"Checkpoint now? If nobody answers within 20 minutes, baton picks Keep going.", "exactly 3 options, in this order",
		`"Checkpoint now" (description: "Finish the current step`, `"Pause baton" (description:`, `"Keep going" (description:`,
		"not multi-select", "baton acts on the answer itself"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("warning lacks %q: %q", want, ctx)
		}
	}
	// The default is third, where the 3 baton types when nobody answers lands (in a permission prompt, 3 is No).
	if i, j, k := strings.Index(ctx, `"Checkpoint now"`), strings.Index(ctx, `"Pause baton"`), strings.Index(ctx, `"Keep going" (`); !(i < j && j < k) {
		t.Fatalf("options out of order: %q", ctx)
	}
	// It includes the checkpoint option, so the nudge does not follow it; nor does it repeat.
	if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil {
		t.Fatalf("asked twice: %v", out)
	}
	// After a compaction (or anything else that brings the context well down), it re-arms.
	f.setContext(600_000)
	f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})
	f.setContext(740_000)
	if !strings.Contains(additionalContext(f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})), "AskUserQuestion") {
		t.Fatal("the re-armed warning did not fire")
	}
}

// The question says when baton answers it, from the timeout the host runs with.
func TestContextWarningSaysWhenBatonAnswers(t *testing.T) {
	f := newFixture(t, true)
	f.vars = map[string]string{"BATON_COMPACT_CAP": "810000", "BATON_WARN_TIMEOUT": "45s"}
	f.setContext(730_000)
	if ctx := additionalContext(f.fire("PostToolUse", map[string]any{"tool_name": "Bash"})); !strings.Contains(ctx, "If nobody answers within 45s, baton picks Keep going") {
		t.Fatalf("warning: %q", ctx)
	}
}

func TestContextWarningStaysQuiet(t *testing.T) {
	for _, c := range []struct {
		name string
		vars map[string]string
		set  func(*state.State)
	}{
		{"compaction owed", defaultCap, func(st *state.State) { st.CheckpointOwed = true }},
		{"compaction underway", defaultCap, func(st *state.State) { st.Run.Compaction.Status = state.CompactActive }},
		{"paused", defaultCap, func(st *state.State) { st.Mode = state.ModePaused }},
		{"turned off", map[string]string{"BATON_COMPACT_CAP": "810000", "BATON_WARN_PCT": "0", "BATON_CHECKPOINT_PCT": "0"}, func(*state.State) {}},
	} {
		f := newFixture(t, true)
		f.vars = c.vars
		f.setContext(760_000)
		f.store.Update(func(st *state.State) error { c.set(st); return nil })
		if out := f.fire("PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil {
			t.Errorf("%s: %v", c.name, out)
		}
	}
}

func TestToolsAreHeldWhileACompactionIsOwed(t *testing.T) {
	f := newFixture(t, true)
	if out := f.fire("PreToolUse", map[string]any{"tool_name": "Edit"}); out != nil {
		t.Fatalf("held with nothing owed: %v", out)
	}
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, false); return err })
	out := f.fire("PreToolUse", map[string]any{"tool_name": "Edit"})
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" || !strings.Contains(hso["permissionDecisionReason"].(string), "End your turn now") {
		t.Fatalf("not held: %v", out)
	}
	out = f.fire("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "baton status"}})
	if hso, _ := out["hookSpecificOutput"].(map[string]any); hso["permissionDecision"] != "allow" {
		t.Fatalf("baton's own CLI was held: %v", out)
	}
	if out := f.fire("PreToolUse", map[string]any{"tool_name": "Edit", "agent_id": "sub"}); out != nil {
		t.Fatalf("a subagent was held: %v", out)
	}
}

// warned issues the context question the way a run does (the context past the warning line) and
// returns it.
func (f *fixture) warned() string {
	f.t.Helper()
	f.vars = defaultCap
	f.setContext(730_000)
	f.fire("PostToolUse", bash("ls"))
	q := f.state().Run.WarnQuestion
	if q == "" {
		f.t.Fatal("no context question issued")
	}
	return q
}

// blocked raises a blocked escalation the way a run does (baton blocked, then a stop) and returns the
// question baton issued for it.
func (f *fixture) blocked(reason string) string {
	f.t.Helper()
	f.store.Update(func(st *state.State) error { state.SetBlocked(st, reason, f.now); return nil })
	f.fire("Stop", map[string]any{})
	e := f.state().Run.Escalation
	if e == nil || e.Question == "" {
		f.t.Fatalf("no escalation question issued: %+v", e)
	}
	return e.Question
}

var escalationLabels = []string{"Continue", "Pause baton"}
var warnLabels = []string{"Checkpoint now", "Pause baton", "Keep going"}

// The human's answer to baton's own question acts by itself: the model need not remember to run anything.
func TestAnswersToBatonsQuestionsAct(t *testing.T) {
	reply := func(f *fixture, q string, labels []string, a string) map[string]any {
		return f.fire("PostToolUse", answer(ask(q, labels...), a))
	}
	f := newFixture(t, true)
	q := f.blocked("need a key")
	if q != "baton: blocked on P0: need a key" {
		t.Fatalf("question %q", q)
	}
	if out := reply(f, q, escalationLabels, "Continue"); !strings.Contains(additionalContext(out), "baton has resumed") {
		t.Fatalf("continue: %v", out)
	}
	if st := f.state(); st.Blocked != nil || st.Run.Escalation != nil || st.Mode != state.ModeRunning {
		t.Fatalf("not resumed: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(f.store.Dir, "events.jsonl")); !strings.Contains(string(b), `"kind":"resumed"`) {
		t.Fatalf("events: %s", b)
	}
	q = f.blocked("something")
	reply(f, q, escalationLabels, "Pause baton")
	if f.state().Mode != state.ModePaused {
		t.Fatal("not paused")
	}

	// An answer the context question does not offer (typed by the human) does nothing, but it is an answer.
	f = newFixture(t, true)
	if out := reply(f, f.warned(), warnLabels, "Continue"); out != nil || f.state().Run.WarnQuestion != "" {
		t.Fatalf("acted, or the question is still open: %v", out)
	}
	f = newFixture(t, true)
	reply(f, f.warned(), warnLabels, "Checkpoint now")
	if st := f.state(); !st.CheckpointAsked || st.Run.WarnQuestion != "" {
		t.Fatalf("checkpoint not recorded, or the question still open: %+v", st)
	}
	// The model ends its turn without running baton checkpoint: the stop is a checkpoint anyway.
	f.fire("Stop", map[string]any{})
	if c := f.state().Run.Compaction; c.Status != state.CompactQueued || c.Reason != "checkpoint" {
		t.Fatalf("no checkpoint at the stop: %+v", c)
	}

	// The context question has Pause baton too.
	f = newFixture(t, true)
	reply(f, f.warned(), warnLabels, "Pause baton")
	if f.state().Mode != state.ModePaused {
		t.Fatal("not paused")
	}

	// Somebody else's question is none of baton's business, and neither is one that only looks like
	// baton's: nothing was issued.
	f = newFixture(t, true)
	f.store.Update(func(st *state.State) error { state.SetBlocked(st, "need a key", f.now); return nil })
	for _, q := range []string{"Which color?", "baton: blocked on P0: need a key"} {
		if out := reply(f, q, escalationLabels, "Continue"); out != nil || f.state().Blocked == nil {
			t.Fatalf("acted on %q: %v", q, out)
		}
	}
}

// Claude Code's idle notification proves no turn or dialog is open: flags that stuck (an interrupted
// turn fires no Stop) are corrected.
func TestIdleNotificationCorrectsStuckFlags(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		st.Run.TurnOpen = true
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash"})
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "sub"})
		return nil
	})
	f.fire("Notification", map[string]any{"notification_type": "idle_prompt", "message": "Claude is waiting for your input"})
	if r := f.state().Run; r.TurnOpen || r.Dialogs.AnyOpen() {
		t.Fatalf("still stuck: %+v", r)
	}
}

// A Stop recounts subagents from what is really running; one that died in an API error never fired
// SubagentStop.
func TestStopRecountsSubagents(t *testing.T) {
	f := newFixture(t, true)
	f.fire("SubagentStart", map[string]any{"agent_id": "a"})
	f.fire("SubagentStart", map[string]any{"agent_id": "b"})
	f.fire("Stop", map[string]any{"background_tasks": []any{map[string]any{"id": "x", "type": "subagent", "status": "running"}}})
	if n := f.state().Run.Subagents; n != 1 {
		t.Fatalf("subagents = %d", n)
	}
}

// While a plan runs, the only question allowed is one baton issued, exactly as issued. A human-started
// turn, and a paused or idle baton, are left alone.
func TestModelQuestionsAreRefusedWhileAPlanRuns(t *testing.T) {
	decide := func(f *fixture, in map[string]any) (string, string) {
		out := f.fire("PreToolUse", in)
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		d, _ := hso["permissionDecision"].(string)
		why, _ := hso["permissionDecisionReason"].(string)
		return d, why
	}
	f := newFixture(t, true)
	if d, why := decide(f, ask("Which database should I use?", "Postgres", "SQLite")); d != "deny" || !strings.Contains(why, "Decide yourself") {
		t.Fatalf("model question: %q %q", d, why)
	}
	// The v0.1 loophole: anything that started with "baton:" passed.
	if d, _ := decide(f, ask("baton: may I skip the tests?", "Continue", "Pause baton")); d != "deny" {
		t.Fatalf("a baton:-prefixed question baton never issued: %q", d)
	}

	q := f.blocked("need a key")
	if d, _ := decide(f, ask(q, escalationLabels...)); d != "" {
		t.Fatalf("baton's question, as issued: %q", d)
	}
	if d, _ := decide(f, ask(strings.Replace(q, " ", "\n  ", 2), escalationLabels...)); d != "" {
		t.Fatalf("baton's question, rewrapped: %q", d)
	}
	multi := ask(q, escalationLabels...)
	multi["tool_input"].(map[string]any)["questions"].([]any)[0].(map[string]any)["multiSelect"] = true
	two := ask(q, escalationLabels...)
	ti := two["tool_input"].(map[string]any)
	ti["questions"] = append(ti["questions"].([]any), map[string]any{"question": "And?"})
	for name, in := range map[string]map[string]any{
		"reordered":                            ask(q, "Pause baton", "Continue"),
		"an option more":                       ask(q, "Continue", "Pause baton", "Skip it"),
		"relabeled":                            ask(q, "Carry on", "Pause baton"),
		"reworded":                             ask(q+" Please hurry.", escalationLabels...),
		"multi-select":                         multi,
		"two questions":                        two,
		"another question while baton's waits": ask("Which database should I use?", "Postgres", "SQLite"),
	} {
		d, why := decide(f, in)
		if d != "deny" || !strings.Contains(why, `exactly one question, "`+q+`" (header "baton"), and exactly 2 options, in this order: "Continue"`) {
			t.Errorf("%s: %q %q", name, d, why)
		}
	}

	f.store.Update(func(st *state.State) error { st.Run.TurnBy = "human"; return nil })
	if d, _ := decide(f, ask("Which database should I use?", "Postgres", "SQLite")); d != "" {
		t.Fatalf("human-started turn: %q", d)
	}
	// Even then, a question passed off as baton's must be the one baton issued: Haiku dropped the
	// question's last sentence once, and the host never recognized the dialog.
	if d, why := decide(f, ask(strings.TrimSuffix(q, " need a key")+"?", escalationLabels...)); d != "deny" || !strings.Contains(why, `"`+q+`"`) {
		t.Fatalf("a trimmed baton question in a human-started turn: %q %q", d, why)
	}
	f.store.Update(func(st *state.State) error { st.Run.TurnBy = "baton"; st.Mode = state.ModePaused; return nil })
	if d, _ := decide(f, ask("Which database should I use?", "Postgres", "SQLite")); d != "" {
		t.Fatalf("paused: %q", d)
	}
	if b, _ := os.ReadFile(filepath.Join(f.store.Dir, "events.jsonl")); strings.Count(string(b), `"kind":"question_refused"`) != 10 {
		t.Fatalf("events: %s", b)
	}
}

// The host may answer only a dialog the hooks marked as baton's, and they mark only the exact question
// baton issued, asked by the main agent.
func TestOnlyTheIssuedQuestionIsMarkedAsBatons(t *testing.T) {
	kinds := func(f *fixture) []string {
		var out []string
		for _, d := range f.state().Run.Dialogs {
			out = append(out, d.Kind)
		}
		return out
	}
	f := newFixture(t, true)
	wq := f.warned()
	f.fire("PermissionRequest", ask(wq, "Keep going", "Pause baton", "Checkpoint now")) // reordered (a human-started turn lets it through)
	f.fire("PermissionRequest", byAgent(ask(wq, warnLabels...), "sub"))
	f.fire("PermissionRequest", ask("baton: the context holds 1 token. Keep going?", warnLabels...))
	if got := kinds(f); !equal(got, []string{"", "", ""}) {
		t.Fatalf("kinds %q", got)
	}
	f.fire("PermissionRequest", ask(wq, warnLabels...))
	if got := kinds(f); !equal(got, []string{"", "", "", "context_warning"}) {
		t.Fatalf("kinds %q", got)
	}
	eq := f.blocked("need a key") // the stop sweeps the main agent's dialogs; the subagent's stays
	f.fire("PermissionRequest", ask(eq, escalationLabels...))
	if got := kinds(f); !equal(got, []string{"", "escalation"}) {
		t.Fatalf("kinds %q", got)
	}
}

// A reason with quotes, backslashes and line breaks still yields a question the model can repeat
// exactly: what it is shown, quoted, is what baton stored and matches.
func TestAnAwkwardReasonStillMatches(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		state.SetBlocked(st, "need the \"prod\" key\nfrom C:\\vault\tnow", f.now)
		return nil
	})
	out := f.fire("Stop", map[string]any{})
	q := f.state().Run.Escalation.Question
	if q != "baton: blocked on P0: need the 'prod' key from C:/vault now" {
		t.Fatalf("question %q", q)
	}
	if why, _ := out["reason"].(string); !strings.Contains(why, `"`+q+`"`) {
		t.Fatalf("the model is not shown the question verbatim: %q", why)
	}
	if out := f.fire("PreToolUse", ask(q, escalationLabels...)); out != nil {
		t.Fatalf("refused: %v", out)
	}
	f.fire("PermissionRequest", ask(q, escalationLabels...))
	if d, _ := f.state().Run.Dialogs.Only(); d.Kind != "escalation" {
		t.Fatalf("dialog %+v", d)
	}
	f.fire("PostToolUse", answer(ask(q, escalationLabels...), "Continue"))
	if st := f.state(); st.Blocked != nil || st.Run.Dialogs.AnyOpen() {
		t.Fatalf("not resumed: %+v", st)
	}
}

// Hook inputs for the dialog tests, shaped like Claude Code's (spikes/16-escalation, S1).
func ask(q string, labels ...string) map[string]any {
	var opts []any
	for _, l := range labels {
		opts = append(opts, map[string]any{"label": l, "description": l})
	}
	return map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": q, "header": "baton", "options": opts, "multiSelect": false}}}}
}

func bash(cmd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd, "description": "run " + cmd}}
}

func byAgent(in map[string]any, agent string) map[string]any {
	in["agent_id"], in["agent_type"] = agent, "general-purpose"
	return in
}

// answer is the PostToolUse of a question: its input gains the answers, and so does its response.
func answer(in map[string]any, a string) map[string]any {
	ti := in["tool_input"].(map[string]any)
	q := ti["questions"].([]any)[0].(map[string]any)["question"].(string)
	answers := map[string]any{q: a}
	out := map[string]any{"tool_name": "AskUserQuestion", "tool_response": map[string]any{"questions": ti["questions"], "answers": answers},
		"tool_input": map[string]any{"questions": ti["questions"], "answers": answers, "annotations": map[string]any{}}}
	return out
}

func openDialogs(f *fixture) []string {
	var out []string
	for _, d := range f.state().Run.Dialogs {
		out = append(out, d.Agent+"/"+d.Tool)
	}
	return out
}

// The dialog queue through the hooks, in both orders spikes/16-escalation measured. A call's result
// closes its own dialog and nothing else; the rest is swept when its owner is provably done.
func TestDialogQueueThroughTheHooks(t *testing.T) {
	// S1-C: the question first, then a background subagent's permission prompt behind it.
	f := newFixture(t, true)
	f.fire("UserPromptSubmit", map[string]any{"prompt": "go"})
	f.fire("PermissionRequest", ask("probe: background?", "Alpha", "Beta", "Gamma"))
	f.fire("PermissionRequest", byAgent(bash("sleep 3 && touch probe_c.txt"), "sub"))
	if got := openDialogs(f); len(got) != 2 || got[0] != "/AskUserQuestion" || got[1] != "sub/Bash" {
		t.Fatalf("S1-C queue %v", got)
	}
	f.fire("PostToolUse", answer(ask("probe: background?", "Alpha", "Beta", "Gamma"), "Gamma"))
	if got := openDialogs(f); len(got) != 1 || got[0] != "sub/Bash" {
		t.Fatalf("after the answer %v", got)
	}
	f.fire("PostToolUse", byAgent(bash("sleep 3 && touch probe_c.txt"), "sub"))
	if got := openDialogs(f); len(got) != 0 {
		t.Fatalf("after the subagent's command ran %v", got)
	}

	// S1-D: the subagent asked first. Its prompt is on screen; the human said No to it (no hook fires),
	// and only its SubagentStop shows it is gone.
	f = newFixture(t, true)
	f.fire("UserPromptSubmit", map[string]any{"prompt": "go"})
	f.fire("PermissionRequest", byAgent(bash("touch probe_d.txt"), "sub"))
	f.fire("PermissionRequest", ask("probe: queued?", "Alpha", "Beta", "Gamma"))
	if d, _ := f.state().Run.Dialogs.Front(); d.Agent != "sub" {
		t.Fatalf("front %+v", d)
	}
	f.fire("SubagentStop", map[string]any{"agent_id": "sub"})
	if d, ok := f.state().Run.Dialogs.Only(); !ok || d.Tool != "AskUserQuestion" {
		t.Fatalf("after SubagentStop %v", openDialogs(f))
	}
}

func TestDialogsCloseOnlyOnAnExactMatch(t *testing.T) {
	cases := []struct {
		name   string
		result map[string]any
		event  string
	}{
		{"a call that never had a dialog", bash("ls"), "PostToolUse"},
		{"the same command from a subagent", byAgent(bash("make deploy"), "sub"), "PostToolUse"},
		{"another tool", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"command": "make deploy", "description": "run make deploy"}}, "PostToolUseFailure"},
		{"a denial of something else", bash("rm -rf build"), "PermissionDenied"},
	}
	for _, c := range cases {
		f := newFixture(t, true)
		f.fire("PermissionRequest", bash("make deploy"))
		f.fire(c.event, c.result)
		if got := openDialogs(f); len(got) != 1 {
			t.Errorf("%s closed the dialog: %v", c.name, got)
		}
	}
	for _, ev := range []string{"PostToolUse", "PostToolUseFailure", "PermissionDenied"} {
		f := newFixture(t, true)
		f.fire("PermissionRequest", bash("make deploy"))
		f.fire(ev, bash("make deploy"))
		if got := openDialogs(f); len(got) != 0 {
			t.Errorf("%s of the same call left %v", ev, got)
		}
	}
}

// A plan approval's result has an empty tool_input (docs/research/sessions.md), so it cannot be matched
// by its input. It still closes its dialog; and a plan the human sent back, which fires no hook, leaves
// no second entry behind when the model asks again.
func TestPlanApprovalClosesItsDialog(t *testing.T) {
	f := newFixture(t, true)
	plan := func(text string) map[string]any {
		return map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": text, "planFilePath": "/p/x.md"}}
	}
	f.fire("PermissionRequest", plan("first draft"))
	f.fire("PermissionRequest", plan("second draft")) // the first was sent back
	if got := openDialogs(f); !equal(got, []string{"/ExitPlanMode"}) {
		t.Fatalf("after asking twice: %v", got)
	}
	f.fire("PostToolUse", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{},
		"tool_response": map[string]any{"plan": "second draft", "filePath": "/p/x.md"}})
	if got := openDialogs(f); len(got) != 0 {
		t.Fatalf("after approval: %v", got)
	}
}

// What proves a dialog is gone: the main turn ending (Stop, StopFailure) or a new prompt closes the main
// agent's; a subagent's stays until that subagent stops or the session goes idle.
func TestDialogSweeps(t *testing.T) {
	for _, c := range []struct {
		event string
		input map[string]any
		want  []string
	}{
		{"Stop", map[string]any{}, []string{"sub/Bash"}},
		{"StopFailure", map[string]any{"error": "overloaded"}, []string{"sub/Bash"}},
		{"UserPromptSubmit", map[string]any{"prompt": "what now?"}, []string{"sub/Bash"}},
		{"SubagentStop", map[string]any{"agent_id": "sub"}, []string{"/Bash"}},
		{"SubagentStop", map[string]any{"agent_id": "other"}, []string{"/Bash", "sub/Bash"}},
		{"Notification", map[string]any{"notification_type": "idle_prompt"}, nil},
		{"Notification", map[string]any{"notification_type": "permission_prompt"}, []string{"/Bash", "sub/Bash"}},
		{"SessionStart", map[string]any{"source": "resume"}, nil},
		{"SessionStart", map[string]any{"source": "compact"}, []string{"/Bash", "sub/Bash"}},
	} {
		f := newFixture(t, true)
		f.fire("PermissionRequest", bash("make"))
		f.fire("PermissionRequest", byAgent(bash("make"), "sub"))
		f.fire(c.event, c.input)
		if got := openDialogs(f); !equal(got, c.want) {
			t.Errorf("%s %v: %v, want %v", c.event, c.input, got, c.want)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// An answer baton typed itself is recorded as baton's, not the human's.
func TestAnAnswerBatonTypedIsAttributedToTheTimeout(t *testing.T) {
	for _, auto := range []bool{false, true} {
		f := newFixture(t, true)
		q := f.warned()
		f.fire("PermissionRequest", ask(q, "Checkpoint now", "Pause baton", "Keep going"))
		if auto {
			f.store.Update(func(st *state.State) error { st.Run.Dialogs[0].AutoAnswered = f.now; return nil })
		}
		out := f.fire("PostToolUse", answer(ask(q, "Checkpoint now", "Pause baton", "Keep going"), "Keep going"))
		b, _ := os.ReadFile(filepath.Join(f.store.Dir, "events.jsonl"))
		want := map[bool]string{false: `"by":"human"`, true: `"by":"timeout"`}[auto]
		if !strings.Contains(string(b), `"kind":"answered"`) || !strings.Contains(string(b), want) {
			t.Fatalf("auto=%v events: %s", auto, b)
		}
		if say, _ := out["systemMessage"].(string); auto != strings.Contains(say, "nobody answered") {
			t.Fatalf("auto=%v said %q", auto, say)
		}
		// Only the human's own answer counts as the human taking part.
		if human := f.state().Run.HumanAt; human.Equal(f.now) == auto {
			t.Fatalf("auto=%v: human at %v", auto, human)
		}
	}
}

// The human takes part by typing in the session; baton's own nudges and other notifications do not count.
func TestTheHumanTakesPartByTyping(t *testing.T) {
	f := newFixture(t, true)
	for _, prompt := range []string{NudgePrefix + " Continue P0.", "<task-notification>build done</task-notification>"} {
		f.fire("UserPromptSubmit", map[string]any{"prompt": prompt})
		if h := f.state().Run.HumanAt; !h.IsZero() {
			t.Fatalf("%q counted as the human: %v", prompt, h)
		}
	}
	f.now = f.now.Add(time.Minute)
	f.fire("UserPromptSubmit", map[string]any{"prompt": "use the staging key instead"})
	if h := f.state().Run.HumanAt; !h.Equal(f.now) {
		t.Fatalf("human at %v, want %v", h, f.now)
	}
}
