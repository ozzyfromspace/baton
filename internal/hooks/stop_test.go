package hooks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

var tStop = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func demoPlan() plan.Plan {
	return plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "First"}, {ID: "P1", Title: "Second"}}}
}

func running() state.State {
	return state.Attach(demoPlan(), tStop, state.Origin{})
}

func TestStopDecisionTable(t *testing.T) {
	pl := demoPlan()
	cases := []struct {
		name    string
		setup   func(*state.State)
		block   bool   // the stop is refused
		reason  string // substring of the refusal reason
		say     string // substring of the systemMessage
		compact string // compaction reason queued ("" = none)
		notice  string // kind of out-of-band notice queued
		detail  string // substring of the notice's text
	}{
		{name: "idle", setup: func(st *state.State) { *st = state.New() }},
		{name: "paused", setup: func(st *state.State) { st.Mode = state.ModePaused }},
		{name: "phase done queues the boundary compaction",
			setup: func(st *state.State) { state.Done(st, pl, "P0", tStop, false) },
			say:   "compacting, then P1 (Second) starts", compact: "boundary"},
		{name: "checkpoint", setup: func(st *state.State) { st.CheckpointOwed = true }, say: "checkpoint → compacting", compact: "checkpoint"},
		{name: "compaction already in flight",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, false)
				st.Run.Compaction = state.Compaction{Epoch: 3, Status: state.CompactTyped}
			}},
		{name: "a review comes before the boundary", block: true, reason: "P0 has made 0 decisions without you", notice: "review",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, false)
				state.DueReview(st, "P0", tStop)
			}},
		{name: "a proposal not yet put to the human", block: true, reason: "You stopped, but put your proposal d1 to the human first: call the AskUserQuestion tool",
			setup: func(st *state.State) {
				state.AddProposal(st, state.Decision{What: "skip it", Question: "baton: x. Unless you answer by 12:05, I will skip it."}, tStop)
			}},
		{name: "an unasked proposal escalates after repeated stops", block: true, reason: "AskUserQuestion", notice: "stalled",
			setup: func(st *state.State) {
				state.AddProposal(st, state.Decision{What: "skip it", Question: "baton: x."}, tStop)
				st.Run.StopBlocks = MaxStopBlocks
			}},
		{name: "a held proposal waits on the human, asked already", say: "waiting on you — proposal d1 waits for you",
			setup: func(st *state.State) {
				st.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "proposal d1 waits for you: skip it", Since: tStop, Asked: true}
			}},
		{name: "blocked asks the human once",
			setup: func(st *state.State) { state.SetBlocked(st, "need a decision", tStop) },
			block: true, reason: "AskUserQuestion", notice: "blocked"},
		{name: "declared wait in its window",
			setup: func(st *state.State) { state.SetWaiting(st, "the build", time.Hour, tStop) },
			say:   "waiting for the build"},
		{name: "background work is not a status: declare the wait",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{Type: "shell", Status: "running", Description: "npm run dev"}}
			},
			block: true, reason: "Background work is still running (npm run dev), but that is not a status"},
		{name: "Claude Code's own housekeeping is not background work", block: true, reason: "not reported done",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{Type: "dream"}, {Type: "auto-mode scan"}}
			}},
		{name: "monitors alone are not busy", block: true, reason: "not reported done",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{Type: "monitor", Status: "running"}}
			}},
		{name: "a human-started turn still needs a status", setup: func(st *state.State) { st.Run.TurnBy = "human" },
			block: true, reason: "answer it first"},
		{name: "no status is refused", block: true, reason: "baton done P0"},
		{name: "expired wait is no status", block: true, reason: "not reported done",
			setup: func(st *state.State) { state.SetWaiting(st, "x", time.Minute, tStop.Add(-time.Hour)) }},
		{name: "fourth silent stop escalates", block: true, reason: "AskUserQuestion", notice: "stalled",
			setup: func(st *state.State) { st.Run.StopBlocks = MaxStopBlocks }},
		{name: "plan complete notifies once",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, false)
				state.Start(st, tStop, state.Origin{}) // the boundary compaction happened
				state.Done(st, pl, "P1", tStop, false)
			},
			say: "plan complete", notice: "plan_complete"},
		{name: "plan complete counts the decisions made without the human",
			setup: func(st *state.State) {
				state.AddNote(st, "a", "", tStop)
				state.Done(st, pl, "P0", tStop, false)
				state.Start(st, tStop, state.Origin{})
				state.AddProposal(st, state.Decision{What: "b"}, tStop)
				state.Resolve(st, "d2", state.ByTimeout, "Go ahead", tStop)
				state.AddProposal(st, state.Decision{What: "c"}, tStop)
				state.Resolve(st, "d3", state.ByHuman, "Go ahead", tStop) // the human said so: not without them
				state.Done(st, pl, "P1", tStop, false)
			},
			say: "plan complete — all 2 phases done · 2 decisions made without you — /baton status", notice: "plan_complete",
			detail: "Demo: all 2 phases done · 2 decisions made without you — /baton status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := running()
			if c.setup != nil {
				c.setup(&st)
			}
			d := decideStop(&st, pl, tStop, decide.Words{Git: true})
			blocked := d.output["decision"] == "block"
			if blocked != c.block {
				t.Fatalf("blocked = %v, want %v (%v)", blocked, c.block, d.output)
			}
			if c.reason != "" && !strings.Contains(d.output["reason"].(string), c.reason) {
				t.Errorf("reason %q lacks %q", d.output["reason"], c.reason)
			}
			if c.say != "" {
				if msg, _ := d.output["systemMessage"].(string); !strings.Contains(msg, c.say) {
					t.Errorf("systemMessage %q lacks %q", msg, c.say)
				}
			}
			if c.compact != "" {
				if st.Run.Compaction.Status != state.CompactQueued || st.Run.Compaction.Reason != c.compact {
					t.Errorf("compaction = %+v, want queued %s", st.Run.Compaction, c.compact)
				}
			} else if c.name != "compaction already in flight" && st.Run.Compaction.Status == state.CompactQueued {
				t.Errorf("unexpected compaction queued")
			}
			if c.notice != "" && (len(st.Run.Notices) != 1 || st.Run.Notices[0].Kind != c.notice) {
				t.Errorf("notices = %+v, want one %s", st.Run.Notices, c.notice)
			} else if c.detail != "" && !strings.Contains(st.Run.Notices[0].Text, c.detail) {
				t.Errorf("notice %q lacks %q", st.Run.Notices[0].Text, c.detail)
			}
			if c.notice == "" && len(st.Run.Notices) != 0 {
				t.Errorf("unexpected notices %+v", st.Run.Notices)
			}
		})
	}
}

func TestEscalationIsAskedOnceAndPushedOnce(t *testing.T) {
	pl := demoPlan()
	st := running()
	state.SetBlocked(&st, "need a decision", tStop)
	first := decideStop(&st, pl, tStop, decide.Words{Git: true})
	second := decideStop(&st, pl, tStop.Add(time.Minute), decide.Words{Git: true})
	if first.output["decision"] != "block" || second.output["decision"] == "block" {
		t.Fatalf("first %v second %v", first.output, second.output)
	}
	if len(st.Run.Notices) != 1 {
		t.Fatalf("pushed %d times", len(st.Run.Notices))
	}
	if !strings.Contains(second.output["systemMessage"].(string), "waiting on you") {
		t.Fatalf("second stop: %v", second.output)
	}
}

func TestRefusalsCountThenEscalate(t *testing.T) {
	pl := demoPlan()
	st := running()
	for i := 1; i <= MaxStopBlocks; i++ {
		if d := decideStop(&st, pl, tStop, decide.Words{Git: true}); d.output["decision"] != "block" || st.Run.StopBlocks != i {
			t.Fatalf("stop %d: %v (blocks %d)", i, d.output, st.Run.StopBlocks)
		}
	}
	d := decideStop(&st, pl, tStop, decide.Words{Git: true})
	if !strings.Contains(d.output["reason"].(string), "AskUserQuestion") || st.Run.Escalation == nil {
		t.Fatalf("escalation: %v", d.output)
	}
}

// fireCode is fire for handlers that may exit 2 (asyncRewake hooks).
func (f *fixture) fireCode(event string, input map[string]any) (int, string) {
	f.t.Helper()
	input["hook_event_name"] = event
	if _, ok := input["session_id"]; !ok {
		input["session_id"] = f.sid
	}
	raw, _ := json.Marshal(input)
	var out, errb bytes.Buffer
	h := Handlers(Deps{Open: OpenRun(f.clock)})
	code := Dispatch(event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), func() time.Time { return f.now }, h)
	return code, errb.String()
}

func TestBoundarySequenceThroughTheHooks(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, false); return err })

	out := f.fire("Stop", map[string]any{"stop_hook_active": false})
	if out["decision"] == "block" || f.state().Run.Compaction.Status != state.CompactQueued {
		t.Fatalf("stop: %v %+v", out, f.state().Run.Compaction)
	}
	// The host types /compact and records it; then Claude Code runs the compaction hooks.
	f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	brief := f.fire("SessionStart", map[string]any{"source": "compact"})
	ctx := brief["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "Now: P1 — Second. Begin it now.") || !strings.Contains(ctx, "Do b.") {
		t.Fatalf("brief:\n%s", ctx)
	}
	if st := f.state(); st.BoundaryOwed || st.Phases["P1"].Status != state.PhaseActive {
		t.Fatalf("P1 not started: %+v", st)
	}
	f.fire("PostCompact", map[string]any{"trigger": "manual"})
	code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"})
	if code != 2 || !strings.Contains(msg, "[baton] Context compacted. Begin P1 (Second) now") {
		t.Fatalf("rewake: code %d %q", code, msg)
	}
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 0 {
		t.Fatal("rewoke twice for one compaction")
	}
	// The next stop with nothing owed and no status is refused, not compacted again.
	if out := f.fire("Stop", map[string]any{}); out["decision"] != "block" {
		t.Fatalf("stop after boundary: %v", out)
	}
}

// The phase a boundary starts records what it finds: HEAD and the work already uncommitted, so its own
// blocked and done refuse only what it leaves. Without git there is nothing to record.
func TestABoundaryRecordsWhatTheNextPhaseFinds(t *testing.T) {
	boundary := func(f *fixture) *state.PhaseState {
		pl, _ := f.store.LoadPlan()
		f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, false); return err })
		f.fire("Stop", map[string]any{})
		f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
		f.fire("PreCompact", map[string]any{"trigger": "manual"})
		f.fire("SessionStart", map[string]any{"source": "compact"})
		return f.state().Phases["P1"]
	}
	gittest.Isolate(t)
	f := newFixture(t, true)
	root := filepath.Dir(f.dir)
	gittest.Init(t, root)
	gittest.Commit(t, root, "first", "plan.md")
	gittest.Write(t, root, "left.txt", "uncommitted, from P0\n")
	ps := boundary(f)
	if ps.StartHead != gittest.Git(t, root, "rev-parse", "HEAD") || ps.StartDirty == nil {
		t.Fatalf("P1 started with %+v", ps)
	}
	if files := ps.StartDirty.Files; len(files) != 1 || files["left.txt"] == "" {
		t.Errorf("recorded %v; want left.txt alone (baton's own files are not work)", files)
	}

	plain := newFixture(t, true)
	if ps := boundary(plain); ps.Status != state.PhaseActive || ps.StartHead != "" || ps.StartDirty != nil {
		t.Errorf("without git: %+v", ps)
	}
}

func TestAutoCompactionGetsAContinueBriefAndNoRewake(t *testing.T) {
	f := newFixture(t, true)
	f.fire("PreCompact", map[string]any{"trigger": "auto"})
	brief := f.fire("SessionStart", map[string]any{"source": "compact"})
	ctx := brief["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "compacted the context automatically") || !strings.Contains(ctx, "continue P0 — First") {
		t.Fatalf("brief:\n%s", ctx)
	}
	if code, _ := f.fireCode("PostCompactRewake", map[string]any{"trigger": "auto"}); code != 0 {
		t.Fatal("rewoke after an auto-compaction, which continues by itself")
	}
}
