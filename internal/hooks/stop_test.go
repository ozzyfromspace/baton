package hooks

import (
	"bytes"
	"encoding/json"
	"os"
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
			setup: func(st *state.State) { state.Done(st, pl, "P0", tStop, state.DoneOpts{}) },
			say:   "compacting, then P1 (Second) starts", compact: "boundary"},
		{name: "phase done, and a phase set aside resumes",
			setup: func(st *state.State) {
				st.Phases["P1"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkRed}}
				state.Done(st, pl, "P0", tStop, state.DoneOpts{})
			},
			say: "baton: phase done → compacting, then P1 (Second) resumes", compact: "boundary"},
		{name: "checkpoint", setup: func(st *state.State) { st.CheckpointOwed = true }, say: "checkpoint → compacting", compact: "checkpoint"},
		{name: "compaction already in flight",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, state.DoneOpts{})
				st.Run.Compaction = state.Compaction{Epoch: 3, Status: state.CompactTyped}
			}},
		{name: "a review comes before the boundary", block: true, reason: "P0 has made 0 decisions without you", notice: "review",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, state.DoneOpts{})
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
		{name: "the first stop on background work is refused once", block: true,
			reason: "You stopped while your background work runs (npm test, and 1 more). Keep working on what does not depend on it",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Status: "running", Description: "npm test"}, {ID: "s1", Type: "subagent"}}
			}},
		{name: "a stop on background work already told about is allowed", say: "P0 (First) waits on background work (npm test), which wakes Claude when it ends",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Status: "running", Description: "npm test"}}
				st.Run.BackgroundTold, st.Run.StopBlocks = []string{"b1", "b2"}, 2
			}},
		{name: "background work started since is news again", block: true, reason: "your background work runs (npm test, and 1 more)",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Description: "npm test"}, {ID: "b3", Type: "shell", Description: "npm run lint"}}
				st.Run.BackgroundTold = []string{"b1"}
			}},
		{name: "the phase's job comes before background work", say: "P0 (First) waits on job j1",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Description: "npm test"}}
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
			}},
		{name: "Claude Code's own housekeeping is not background work", block: true, reason: "not reported done",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{Type: "dream"}, {Type: "auto-mode scan"}}
			}},
		{name: "monitors alone are not busy", block: true, reason: "not reported done",
			setup: func(st *state.State) {
				st.Run.Background = []state.Task{{ID: "m1", Type: "shell", Status: "running", Description: "tail -F app.log"}}
				st.Run.Monitors = []string{"m1"}
			}},
		{name: "a human-started turn still needs a status", setup: func(st *state.State) { st.Run.TurnBy = "human" },
			block: true, reason: "answer it first"},
		{name: "no status is refused", block: true, reason: "baton done P0"},
		{name: "a job's result answers the stop", block: true,
			setup: func(st *state.State) { st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Status: state.JobFailed}} }},
		{name: "a result given already does not", block: true, reason: "baton done P0",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Status: state.JobFailed, Delivered: tStop}}
			}},
		{name: "another phase's result does not", block: true, reason: "baton done P0",
			setup: func(st *state.State) { st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Status: state.JobFailed}} }},
		{name: "a result waits for a block", block: true, reason: "AskUserQuestion", notice: "blocked",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Status: state.JobFailed}}
				state.SetBlocked(st, "need a decision", tStop)
			}},
		{name: "nothing in hand: the stop waits for the job", say: "no phase can start until job j1 (`make test`, P0) finishes",
			setup: func(st *state.State) {
				st.Phases["P0"].Status, st.Current = state.PhaseVerifying, ""
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
			}},
		{name: "the phase's job running: the session idles in place", say: "P0 (First) waits on job j1 (`make test`) — nothing is needed from you: Claude is given its result when it ends",
			setup: func(st *state.State) {
				st.Run.StopBlocks = 2
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
			}},
		{name: "a queued job too", say: "P0 (First) waits on job j1",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobQueued}}
			}},
		{name: "another phase's job is no reason to idle", block: true, reason: "baton done P0",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobRunning}}
			}},
		{name: "a proposal comes before idling on a job", block: true, reason: "put your proposal d1 to the human first",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
				state.AddProposal(st, state.Decision{What: "skip it", Question: "baton: x."}, tStop)
			}},
		{name: "a checkpoint comes before idling on a job", say: "checkpoint → compacting", compact: "checkpoint",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning}}
				st.CheckpointOwed = true
			}},
		{name: "a switch queued lets the turn end",
			setup: func(st *state.State) {
				st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, Queued: tStop}}
				st.Run.Compaction.Queue("switch", tStop)
			}},
		{name: "fourth silent stop escalates", block: true, reason: "AskUserQuestion", notice: "stalled",
			setup: func(st *state.State) { st.Run.StopBlocks = MaxStopBlocks }},
		{name: "plan complete notifies once",
			setup: func(st *state.State) {
				state.Done(st, pl, "P0", tStop, state.DoneOpts{})
				state.Start(st, tStop, state.Origin{}) // the boundary compaction happened
				state.Done(st, pl, "P1", tStop, state.DoneOpts{})
			},
			say: "plan complete", notice: "plan_complete"},
		{name: "plan complete counts the decisions made without the human",
			setup: func(st *state.State) {
				state.AddNote(st, "a", "", tStop)
				state.Done(st, pl, "P0", tStop, state.DoneOpts{})
				state.Start(st, tStop, state.Origin{})
				state.AddProposal(st, state.Decision{What: "b"}, tStop)
				state.Resolve(st, "d2", state.ByTimeout, "Go ahead", tStop)
				state.AddProposal(st, state.Decision{What: "c"}, tStop)
				state.Resolve(st, "d3", state.ByHuman, "Go ahead", tStop) // the human said so: not without them
				state.Done(st, pl, "P1", tStop, state.DoneOpts{})
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
			d := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil)
			if delivered := d.deliver.any(); delivered != (c.name == "a job's result answers the stop") {
				t.Errorf("delivered %+v", d.deliver)
			}
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
			} else if c.name != "compaction already in flight" && c.name != "a switch queued lets the turn end" && st.Run.Compaction.Status == state.CompactQueued {
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
			if !blocked && st.Run.StopBlocks != 0 {
				t.Errorf("an allowed stop left the count of stops without status at %d", st.Run.StopBlocks)
			}
		})
	}
}

func TestEscalationIsAskedOnceAndPushedOnce(t *testing.T) {
	pl := demoPlan()
	st := running()
	state.SetBlocked(&st, "need a decision", tStop)
	first := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil)
	second := decideStop(&st, pl, tStop.Add(time.Minute), decide.Words{Git: true}, nil)
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
		if d := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil); d.output["decision"] != "block" || st.Run.StopBlocks != i {
			t.Fatalf("stop %d: %v (blocks %d)", i, d.output, st.Run.StopBlocks)
		}
	}
	d := decideStop(&st, pl, tStop, decide.Words{Git: true}, nil)
	if !strings.Contains(d.output["reason"].(string), "AskUserQuestion") || st.Run.Escalation == nil {
		t.Fatalf("escalation: %v", d.output)
	}
}

// Background work is refused once, then the stops on it are allowed; new work is refused once again, and
// none of it counts toward bringing the human in.
func TestBackgroundWorkIsRefusedOncePerNewTask(t *testing.T) {
	pl := demoPlan()
	st := running()
	stop := func(ids ...string) decision {
		var bg []state.Task
		for _, id := range ids {
			bg = append(bg, state.Task{ID: id, Type: "shell", Description: "task " + id})
		}
		st.Run.SetBackground(bg, tStop)
		return decideStop(&st, pl, tStop, decide.Words{Git: true}, nil)
	}
	for i, c := range []struct {
		ids   []string
		block bool
		event string
	}{
		{[]string{"b1"}, true, "stop_refused"},
		{[]string{"b1"}, false, "idle_on_background"},
		{[]string{"b1"}, false, "idle_on_background"},
		{[]string{"b1", "b2"}, true, "stop_refused"},
		{[]string{"b2"}, false, "idle_on_background"},
	} {
		d := stop(c.ids...)
		if blocked := d.output["decision"] == "block"; blocked != c.block || len(d.events) != 1 || d.events[0].kind != c.event || st.Run.Escalation != nil {
			t.Fatalf("stop %d on %v: %v, events %+v", i+1, c.ids, d.output, d.events)
		}
	}
	if d := stop(); d.output["decision"] != "block" || !strings.Contains(d.output["reason"].(string), "not reported done") || st.Run.StopBlocks != 1 {
		t.Fatalf("once the work ended: %v (blocks %d)", d.output, st.Run.StopBlocks)
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
	f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, state.DoneOpts{}); return err })

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

// A phase reopened after its verification failed comes back through the same boundary, as a resume: its
// brief carries its park notes, and the model is told to resume it rather than begin it.
func TestResumeSequenceThroughTheHooks(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.store.Update(func(st *state.State) error {
		_, err := state.ParkPhase(st, pl, "P0", state.Park{Reason: state.ParkRed, Since: f.now, Notes: "a.go done in 1a2b3c4."})
		return err
	})
	if st := f.state(); st.Current != "P0" || !st.BoundaryOwed {
		t.Fatalf("a reopened phase is not taken up again: %+v", st)
	}
	f.fire("Stop", map[string]any{})
	f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
	f.fire("PreCompact", map[string]any{"trigger": "manual"})
	out := f.fire("SessionStart", map[string]any{"source": "compact"})
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "Now: resume P0 — First. It was finished, but its verification failed") || !strings.Contains(ctx, "a.go done in 1a2b3c4.") {
		t.Fatalf("brief:\n%s", ctx)
	}
	if out["systemMessage"] != "baton: P0 (First) resumes now, from where it was set aside" {
		t.Errorf("announced %q", out["systemMessage"])
	}
	if st := f.state(); st.BoundaryOwed || st.Phases["P0"].Status != state.PhaseActive || !st.Resumed() {
		t.Fatalf("P0 not resumed: %+v", st.Phases["P0"])
	}
	if log := f.eventLog(); !strings.Contains(log, `"kind":"phase_resumed"`) || !strings.Contains(log, `"reason":"red"`) || strings.Contains(log, `"kind":"phase_started"`) {
		t.Errorf("events:\n%s", log)
	}
	f.fire("PostCompact", map[string]any{"trigger": "manual"})
	if code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 2 || !strings.Contains(msg, "[baton] Context compacted. Resume P0 (First) now") {
		t.Fatalf("rewake: code %d %q", code, msg)
	}
}

// The phase a boundary starts records what it finds: HEAD and the work already uncommitted, so its own
// blocked and done refuse only what it leaves. Without git there is nothing to record.
func TestABoundaryRecordsWhatTheNextPhaseFinds(t *testing.T) {
	boundary := func(f *fixture) *state.PhaseState {
		pl, _ := f.store.LoadPlan()
		f.store.Update(func(st *state.State) error { _, err := state.Done(st, pl, "P0", f.now, state.DoneOpts{}); return err })
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

// A switch goes through the hooks like a boundary: the phase taken up begins with a fresh brief, and the
// phase set aside comes back later as a resume, with its job's notes and result.
func TestSwitchSequenceThroughTheHooks(t *testing.T) {
	f := newFixture(t, false)
	doc := []byte("# Demo\n\n## P0 — First\nAfter: none\n\nDo a.\n\n## P1 — Second\nAfter: none\n\nDo b.\n")
	docPath := filepath.Join(filepath.Dir(f.dir), "plan.md")
	os.WriteFile(docPath, doc, 0o644)
	pl, err := plan.Build(docPath, doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	f.store.SavePlan(pl)
	compact := func() map[string]any {
		f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
		f.fire("PreCompact", map[string]any{"trigger": "manual"})
		out := f.fire("SessionStart", map[string]any{"source": "compact"})
		f.fire("PostCompact", map[string]any{"trigger": "manual"})
		return out
	}
	f.store.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, f.now, state.Origin{})
		st.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, Notes: "a.go done; make test checks it", Queued: f.now}}
		if _, ok := state.Switch(st, pl, f.now); !ok {
			t.Fatal("no switch")
		}
		return nil
	})
	out := compact()
	if ctx := additional(out); !strings.Contains(ctx, "[baton] The context was compacted to switch phases: P0 waits on its job j1, so baton set P0 aside and takes it up again once the job ends.") ||
		!strings.Contains(ctx, "Now: P1 — Second. Begin it now.") || !strings.Contains(ctx, "⏸ P0 — First (parked)") || strings.Contains(ctx, "phase boundary") {
		t.Fatalf("brief:\n%s", ctx)
	}
	if ev := f.eventLog(); !strings.Contains(ev, `"kind":"brief","phase":"P1"`) || !strings.Contains(ev, `"type":"switch"`) || !strings.Contains(ev, `"kind":"phase_started","phase":"P1"`) {
		t.Errorf("events:\n%s", ev)
	}
	if code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 2 || !strings.Contains(msg, "[baton] Context compacted. Begin P1 (Second) now") {
		t.Fatalf("rewake: code %d %q", code, msg)
	}

	// P0's gate passes while P1 is under way; once P1 is done, P0 comes back first.
	f.finished("P0", state.JobPassed)
	f.store.Update(func(st *state.State) error {
		st.Jobs = st.Jobs[1:] // the job, as it ended
		_, err := state.Done(st, pl, "P1", f.now, state.DoneOpts{})
		return err
	})
	if out := f.fire("Stop", map[string]any{}); !strings.Contains(said(out), "compacting, then P0 (First)") {
		t.Fatalf("stop: %v", out)
	}
	out = compact()
	ctx := additional(out)
	for _, want := range []string{"Now: resume P0 — First from where it stood", "a.go done; make test checks it", "The result of the job P0 was set aside for:\n\n[baton] Job j1 (P0) passed"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("brief lacks %q:\n%s", want, ctx)
		}
	}
	if code, msg := f.fireCode("PostCompactRewake", map[string]any{"trigger": "manual"}); code != 2 || !strings.Contains(msg, "Resume P0 (First) now") {
		t.Fatalf("rewake: code %d %q", code, msg)
	}
}
