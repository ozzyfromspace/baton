package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
)

const approvedDoc = "# The next campaign\n\n## Standing rules\nBe careful.\n\n## P0 — Instruments\nMeasure first.\n\n## P1 — Fixture\nThen grow it.\n\n## Verification\nAll green.\n"

// writePlan writes a plan document next to the project, as Claude Code writes a session's plan file.
func (f *fixture) writePlan(name, doc string) string {
	path := filepath.Join(filepath.Dir(f.dir), name)
	os.WriteFile(path, []byte(doc), 0o644)
	return path
}

// approve plays the human approving the plan in file: the dialog, then the result.
func (f *fixture) approve(file string) map[string]any {
	f.fire("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "…", "planFilePath": file}})
	return f.fire("PostToolUse", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{},
		"tool_response": map[string]any{"plan": "…", "isAgent": false, "filePath": file}, "permission_mode": "acceptEdits"})
}

func say(out map[string]any) string { s, _ := out["systemMessage"].(string); return s }

func TestAnApprovedPlanIsAttachedAndStartsAfterACompaction(t *testing.T) {
	f := newFixture(t, false)
	file := f.writePlan("humming-whistling-hare.md", approvedDoc)
	out := f.approve(file)
	if !strings.Contains(say(out), `attached "The next campaign" (2 phases; dependencies: each phase follows the one before it) → compacting the planning conversation, then P0 starts`) {
		t.Fatalf("announced %q", say(out))
	}
	if tell := feedback(out); !strings.Contains(tell, "Do not start P0 in this turn: end your turn now") {
		t.Fatalf("told %q", tell)
	}
	st := f.state()
	if st.Mode != state.ModeRunning || st.Current != "P0" || !st.BoundaryOwed || st.Phases["P0"].Status != state.PhasePending {
		t.Fatalf("state %+v", st)
	}
	if pl, _ := f.store.LoadPlan(); pl.File != file || pl.RulesAnchor != "## Standing rules" || pl.EndAnchor != "## Verification" {
		t.Fatalf("plan %+v", pl)
	}
	if !f.logged("attached", map[string]any{"plan": file, "phases": float64(2), "longest_chain": float64(2), "by": "approval"}) {
		t.Errorf("events: %s", f.eventLog())
	}
	if d := denyReason(f.fire("PreToolUse", bash("go test ./..."))); !strings.Contains(d, "baton attached the plan, and compacts the conversation so far before P0 begins") {
		t.Fatalf("P0's work was not held: %q", d)
	}
	if msg := say(f.fire("Stop", map[string]any{})); !strings.Contains(msg, "plan attached → compacting the conversation so far, then P0 (Instruments) starts") {
		t.Fatalf("stop said %q", msg)
	}
	f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
	_, brief := f.compacted("manual")
	for _, want := range []string{`The plan "The next campaign"`, "was attached, and the conversation before it compacted", "Now: P0 — Instruments. Begin it now.", "Be careful.", "Measure first."} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	if f.state().Phases["P0"].Status != state.PhaseActive {
		t.Error("P0 did not start with its brief")
	}
}

// Not every plan is a baton plan: one without phase headings is left alone.
func TestAPlanWithoutPhasesIsLeftAlone(t *testing.T) {
	f := newFixture(t, false)
	out := f.approve(f.writePlan("quick-fix.md", "# Fix the login bug\n\nChange the check in auth.go.\n"))
	if !strings.Contains(say(out), "not attached, as it has no phase headings") || feedback(out) != "" {
		t.Fatalf("said %v", out)
	}
	if st := f.state(); st.Mode != state.ModeIdle {
		t.Fatalf("attached: %+v", st)
	}
}

// A run that has finished is replaced by the next plan approved in the session, with no question.
func TestTheNextPlanReplacesAFinishedRun(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error { st.Mode, st.Current = state.ModeComplete, ""; return nil })
	f.approve(f.writePlan("next.md", approvedDoc))
	if pl, _ := f.store.LoadPlan(); pl.Title != "The next campaign" || f.state().Mode != state.ModeRunning {
		t.Fatalf("plan %+v", pl)
	}
}

// startRun leaves the fixture mid-run on its own plan file: P0 done, P1 under way.
func (f *fixture) startRun() string {
	file := f.writePlan("session-plan.md", "# Demo\n\n## P0 — First\nDo a.\n\n## P1 — Second\nDo b.\n\n## P2 — Third\nDo c.\n")
	f.approve(file)
	f.store.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Phases["P1"].Status = state.PhaseDone, state.PhaseActive
		st.Current, st.BoundaryOwed = "P1", false
		return nil
	})
	return file
}

func TestARevisedPlanApprovedMidRunIsPutToTheHuman(t *testing.T) {
	f := newFixture(t, false)
	file := f.startRun()
	// The human re-planned in the same session: Claude Code rewrote the same file.
	os.WriteFile(file, []byte("# Demo\n\n## P0 — First\nDo a.\n\n## P1 — Second\nDo b, better.\n\n## P2 — Third\nDo c.\n\n## P3 — Fourth\nDo d.\n"), 0o644)
	out := f.approve(file)
	q := "baton: you approved a revised plan while 'Demo' is running (at P1). Carry on with the revision, or start it over from P0?"
	if !strings.Contains(feedback(out), q) || !strings.Contains(say(out), "asking you what to do with it") {
		t.Fatalf("asked %v", out)
	}
	if d := denyReason(f.fire("PreToolUse", bash("make"))); !strings.Contains(d, "put baton's question about the plan the human approved to them first") {
		t.Fatalf("work was not held: %q", d)
	}
	question := ask(q, "Continue with the revised plan", "Start the new plan from P0")
	if d := denyReason(f.fire("PreToolUse", question)); d != "" {
		t.Fatalf("baton's own question was refused: %q", d)
	}
	f.fire("PermissionRequest", question)
	if d, _ := f.state().Run.Dialogs.Front(); d.Kind != "replan" {
		t.Fatalf("dialog %+v", d)
	}
	out = f.fire("PostToolUse", answer(question, "Continue with the revised plan"))
	st := f.state()
	if st.Replan != nil || st.Current != "P1" || st.Phases["P1"].Status != state.PhaseActive || st.Phases["P0"].Status != state.PhaseDone || st.Phases["P3"] == nil {
		t.Fatalf("after revising: %+v", st)
	}
	if !strings.Contains(say(out), "revised — P1 (Second) carries on") {
		t.Fatalf("said %q", say(out))
	}
	if pl, _ := f.store.LoadPlan(); len(pl.Phases) != 4 || pl.SHA256 != pl.SHA() {
		t.Fatalf("plan %+v", pl)
	}
	// The plan as it was before the revision keeps its record.
	if got := f.store.Archives(); len(got) != 1 || got[0].Phases != 3 || got[0].Done != 1 || !f.logged("plan_archived", nil) {
		t.Errorf("archives %+v; events %v", got, f.events())
	}
}

// A revision whose phases left all wait for a verification still running: nothing starts, and the model is
// told so rather than that the plan is done.
func TestARevisionThatMustWaitForAJob(t *testing.T) {
	f := newFixture(t, false)
	file := f.startRun()
	f.store.Update(func(st *state.State) error {
		st.Phases["P1"].Status, st.Current = state.PhaseVerifying, ""
		st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Status: state.JobRunning}}
		return nil
	})
	os.WriteFile(file, []byte("# Demo\n\n## P0 — First\nDo a.\n\n## P1 — Second\nDo b.\n\n## P2 — Third\nDo c, better.\n"), 0o644)
	f.approve(file)
	question := ask(f.state().Replan.Question, "Continue with the revised plan", "Start the new plan from P0")
	f.fire("PermissionRequest", question)
	out := f.fire("PostToolUse", answer(question, "Continue with the revised plan"))
	if st := f.state(); st.Mode != state.ModeRunning || st.Current != "" || st.Phases["P1"].Status != state.PhaseVerifying {
		t.Fatalf("after revising: %+v", st)
	}
	if !strings.Contains(say(out), "no phase of the revision can start until a job finishes") || !strings.Contains(feedback(out), "waits for a job to finish") {
		t.Fatalf("said %q, told %q", say(out), feedback(out))
	}
}

func TestANewPlanApprovedMidRunCanStartOver(t *testing.T) {
	f := newFixture(t, false)
	f.startRun()
	// Planned after a /clear: a new session, so a new plan file; the run's own document is still there.
	file := f.writePlan("after-clear.md", approvedDoc)
	out := f.approve(file)
	q := "baton: you approved a new plan while 'Demo' is running (at P1). Start the new plan from P0, or keep the current run?"
	if !strings.Contains(feedback(out), q) {
		t.Fatalf("asked %q", feedback(out))
	}
	question := ask(q, "Start the new plan from P0", "Keep the current run")
	f.fire("PermissionRequest", question)
	f.fire("PostToolUse", answer(question, "Start the new plan from P0"))
	st := f.state()
	if pl, _ := f.store.LoadPlan(); pl.File != file || st.Current != "P0" || !st.BoundaryOwed || st.Phases["P0"].Status != state.PhasePending {
		t.Fatalf("after starting over: %+v %+v", pl, st)
	}
	if got := f.store.Archives(); len(got) != 1 || got[0].Title != "Demo" {
		t.Errorf("the replaced plan's record: %+v", got)
	}

	// Approved while paused: the human's answer starts it.
	f = newFixture(t, false)
	f.startRun()
	f.store.Update(func(st *state.State) error { return state.Pause(st) })
	f.approve(f.writePlan("after-clear.md", approvedDoc))
	f.fire("PostToolUse", answer(ask(strings.Replace(q, "is running", "is paused", 1), "Start the new plan from P0", "Keep the current run"), "Start the new plan from P0"))
	if st := f.state(); st.Mode != state.ModeRunning || st.Current != "P0" || !st.BoundaryOwed || !f.logged("resumed", map[string]any{"by": "replan"}) {
		t.Fatalf("after starting over from a pause: mode %s current %s; events %v", st.Mode, st.Current, f.events())
	}

	f = newFixture(t, false)
	f.startRun()
	f.approve(f.writePlan("after-clear.md", approvedDoc))
	f.fire("PostToolUse", answer(question, "Keep the current run"))
	if pl, _ := f.store.LoadPlan(); pl.Title != "Demo" || f.state().Current != "P1" || f.state().Replan != nil {
		t.Fatalf("after keeping the run: %+v", pl)
	}
}

func TestAnUnaskedReplanHoldsTheStopUntilTheHumanWrites(t *testing.T) {
	f := newFixture(t, false)
	f.startRun()
	f.approve(f.writePlan("after-clear.md", approvedDoc))
	out := f.fire("Stop", map[string]any{})
	if out["decision"] != "block" || !strings.Contains(out["reason"].(string), "put baton's question about the plan the human approved") {
		t.Fatalf("stop %v", out)
	}
	// Asked enough, the stop goes through and the approval stays: baton answers its default at the
	// deadline (the host's loop), unless the human writes first.
	for range MaxStopBlocks {
		out = f.fire("Stop", map[string]any{})
	}
	if out["decision"] == "block" || f.state().Replan == nil || !strings.Contains(say(out), `baton answers "Keep the current run" at`) ||
		!f.logged("replan_waits", map[string]any{"answer": "Keep the current run"}) || f.logged("escalated", nil) {
		t.Fatalf("stop %v; events:\n%s", out, f.eventLog())
	}
	out = f.fire("UserPromptSubmit", map[string]any{"prompt": "actually, leave it"})
	if f.state().Replan != nil || !strings.Contains(additionalContext(out), "went unanswered, so baton changed nothing") {
		t.Fatalf("after the human wrote: %v", out)
	}
}

// Approved with "clear context": Claude Code ends the session with the approval on screen, starts a new
// one with the plan as its first message, and fires no PostToolUse for the approval.
func TestAPlanApprovedWithClearContextStartsAtOnce(t *testing.T) {
	f := newFixture(t, false)
	file := f.writePlan("clear.md", approvedDoc)
	f.fire("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "…", "planFilePath": file}})
	f.fire("SessionEnd", map[string]any{"reason": "clear"})
	f.fire("SessionStart", map[string]any{"source": "clear", "session_id": "sess-new"})
	out := f.fire("PostToolUse", map[string]any{"session_id": "sess-new", "permission_mode": "acceptEdits", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if !strings.Contains(say(out), "→ P0 starts now") || !strings.Contains(feedback(out), "Work on P0 (Instruments) only.") {
		t.Fatalf("attached? %v", out)
	}
	st := f.state()
	if st.Mode != state.ModeRunning || st.BoundaryOwed || st.Phases["P0"].Status != state.PhaseActive || st.Run.SessionID != "sess-new" {
		t.Fatalf("state %+v", st)
	}

	// A /clear typed after the plan was sent back (Esc fires no hook) is followed by a prompt: no attach.
	f = newFixture(t, false)
	f.fire("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "…", "planFilePath": file}})
	f.fire("SessionEnd", map[string]any{"reason": "clear"})
	f.fire("SessionStart", map[string]any{"source": "clear", "session_id": "sess-new"})
	f.fire("UserPromptSubmit", map[string]any{"session_id": "sess-new", "prompt": "let's start over"})
	f.fire("PostToolUse", map[string]any{"session_id": "sess-new", "permission_mode": "default", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	if st := f.state(); st.Mode != state.ModeIdle {
		t.Fatalf("attached a plan that was sent back: %+v", st)
	}
}

func TestThePlanFileIsWatched(t *testing.T) {
	f := newFixture(t, false)
	file := f.startRun()
	doc, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(strings.Replace(string(doc), "Do c.", "Do c, carefully.", 1)), 0o644)
	if msg := say(f.fire("Stop", map[string]any{})); !strings.Contains(msg, "the plan file changed; every phase still to run is there") {
		t.Fatalf("an edit that keeps the phases: %q", msg)
	}
	if pl, _ := f.store.LoadPlan(); pl.SHA256 != pl.SHA() || f.state().Mode != state.ModeRunning {
		t.Fatal("not followed")
	}
	os.WriteFile(file, []byte("# Something else\n\n## P0 — Other\nx\n"), 0o644)
	msg := say(f.fire("Stop", map[string]any{}))
	if !strings.Contains(msg, "the plan file changed, and baton cannot follow it") || !strings.Contains(msg, "## P1 — Second") ||
		!strings.Contains(msg, "it goes on with the plan as attached") {
		t.Fatalf("a change that loses phases: %q", msg)
	}
	// Nothing waits on the human to fix it: the run goes on with the plan as attached, and they are told.
	if st := f.state(); st.Mode != state.ModeRunning || len(st.Run.Notices) == 0 {
		t.Fatalf("not going on: %+v", st)
	}
	f.fire("Stop", map[string]any{})
	if n := strings.Count(f.eventLog(), `"kind":"plan_changed"`); n != 2 {
		t.Errorf("%d plan_changed events; each change is reported once", n)
	}
}

// A phase the human adds to the plan file while it runs joins the run at the next stop, and is announced.
func TestAPhaseAddedToThePlanFileJoinsTheRun(t *testing.T) {
	f := newFixture(t, false)
	file := f.startRun()
	doc, _ := os.ReadFile(file)
	os.WriteFile(file, append(doc, []byte("\n## P3 — Fourth\nDo d.\n")...), 0o644)
	if msg := say(f.fire("Stop", map[string]any{})); !strings.Contains(msg, "the plan file changed and adds P3 (Fourth); baton follows the new text, and runs it in turn") {
		t.Fatalf("stop said %q", msg)
	}
	pl, _ := f.store.LoadPlan()
	if st := f.state(); pl.Index("P3") != 3 || st.Phases["P3"] == nil || st.Phases["P3"].Status != state.PhasePending || st.Mode != state.ModeRunning {
		t.Fatalf("plan %+v, state %+v", pl.Phases, st.Phases)
	}
	if !strings.Contains(f.eventLog(), `"added":["P3"]`) {
		t.Errorf("events: %s", f.eventLog())
	}
	// P1 is in hand, so P3 waits its turn: nothing else changes.
	if st := f.state(); st.Current != "P1" || st.BoundaryOwed || st.Run.Compaction.InFlight() || f.logged("plan_changed", map[string]any{"next": "P3"}) {
		t.Errorf("with P1 in hand: current %q boundary %v compaction %+v", st.Current, st.BoundaryOwed, st.Run.Compaction)
	}
}

// A phase added while the run holds none, every phase left waiting on a job, starts at the next stop,
// after a compaction, rather than once the job ends.
func TestAPhaseAddedWithNoneInHandStartsAtTheNextStop(t *testing.T) {
	f := newFixture(t, false)
	file := f.startRun()
	f.set(func(st *state.State) {
		st.Phases["P1"].Status, st.Current = state.PhaseVerifying, ""
		st.Jobs = append(st.Jobs, state.Job{ID: "j1", Phase: "P1", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning, Started: f.now})
	})
	doc, _ := os.ReadFile(file)
	os.WriteFile(file, append(doc, []byte("\n## P3 — Fourth\nAfter: P0\n\nDo d.\n")...), 0o644)
	msg := say(f.fire("Stop", map[string]any{}))
	if want := "baton: the plan file changed and adds P3 (Fourth); baton follows the new text\nbaton: P3 can start → compacting, then P3 (Fourth) starts"; !strings.Contains(msg, want) {
		t.Fatalf("stop said %q", msg)
	}
	st := f.state()
	if st.Current != "P3" || !st.BoundaryOwed || st.Run.Compaction.Status != state.CompactQueued || st.Run.Compaction.Reason != "boundary" {
		t.Fatalf("current %q boundary %v compaction %+v", st.Current, st.BoundaryOwed, st.Run.Compaction)
	}
	if st.Phases["P1"].Status != state.PhaseVerifying || st.Phases["P2"].Status != state.PhasePending || st.Job("j1").Status != state.JobRunning {
		t.Errorf("phases %+v, job %+v", st.Phases, st.Job("j1"))
	}
	if !f.logged("plan_changed", map[string]any{"next": "P3"}) || !f.logged("compact_queued", map[string]any{"reason": "boundary", "next": "P3"}) {
		t.Errorf("events: %s", f.eventLog())
	}
}

// The approval says how many workers will work beside the session, and warns when a permission prompt
// would stop them.
func TestAnApprovalSaysHowManyWorkersRun(t *testing.T) {
	doc := strings.Replace(approvedDoc, "Then grow it.", "After: none\n\nThen grow it.", 1)
	for _, c := range []struct {
		name, mode, want string
		warned           bool
	}{
		{"the default mode", "", "baton: up to 1 worker works beside this session on the phases that can go ahead, each in a worktree of its own, " +
			"and baton lands their work on this branch (the plan's widest step is 2 phases; this machine allows 6).", true},
		{"auto mode", "auto", "baton: up to 1 worker works beside this session", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, false)
			f.vars = map[string]string{"HOME": t.TempDir(), "BATON_HOME": t.TempDir(), "BATON_WORKER_MODE": c.mode}
			out := say(f.approve(f.writePlan("p.md", doc)))
			if !strings.Contains(out, "then P0 starts\n"+c.want) || strings.Contains(out, "permission mode") != c.warned {
				t.Errorf("said %q", out)
			}
		})
	}
	f := newFixture(t, false)
	f.vars = map[string]string{"HOME": t.TempDir(), "BATON_HOME": t.TempDir(), "BATON_WORKERS": "0"}
	if out := say(f.approve(f.writePlan("p.md", doc))); strings.Contains(out, "worker") {
		t.Errorf("no workers, and said %q", out)
	}
	f = newFixture(t, false)
	f.vars = map[string]string{"HOME": t.TempDir(), "BATON_HOME": t.TempDir()}
	f.machine = machine.Reading{CPUs: 8, MemTotal: 4 << 30}
	if out := say(f.approve(f.writePlan("p.md", doc))); !strings.Contains(out, "\nbaton: no worker runs beside this session: with workers: auto, a machine needs 4 CPUs and 8 GiB") {
		t.Errorf("a machine too small for a worker, and said %q", out)
	}

	// Approved with "clear context", the plan starts at once, and says so too.
	f = newFixture(t, false)
	f.vars = map[string]string{"HOME": t.TempDir(), "BATON_HOME": t.TempDir()}
	file := f.writePlan("clear.md", doc)
	f.fire("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "…", "planFilePath": file}})
	f.fire("SessionEnd", map[string]any{"reason": "clear"})
	f.fire("SessionStart", map[string]any{"source": "clear", "session_id": "sess-new"})
	out := f.fire("PostToolUse", map[string]any{"session_id": "sess-new", "permission_mode": "acceptEdits", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{}})
	if !strings.Contains(say(out), "→ P0 starts now\nbaton: up to 1 worker works beside this session") {
		t.Errorf("said %q", say(out))
	}
}
