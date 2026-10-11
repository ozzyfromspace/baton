package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// workerDoc is a plan whose P2 builds on P0 alone; P1 waits for nothing.
const workerDoc = "# Demo\n\n## Standing rules\n\nBe kind.\n\n## P0 — First\n\nAfter: none\n\nDo a.\n\n## P1 — Second\n\nAfter: none\n\nDo b.\n\n" +
	"## P2 — Third\n\nAfter: P0\n\nDo c.\n\n## Verification\n\nAll green.\n"

// newWorker is a run (sess-1, on host inst-1, the lead on P0) with a worker on P2 in a git worktree of
// its own (tree, on the branch baton/sess-1/P2), set up as the host sets a worker up before its session
// starts. w fires hooks as the worker's session, lead as the lead's. The plan file lives outside the
// project, as plan files do.
func newWorker(t *testing.T) (w, lead *fixture, tree string) {
	t.Helper()
	return newWorkerIn(t, func(string) string { return t.TempDir() })
}

// newWorkerIn is newWorker with the worktree where in, given the project's checkout, says.
func newWorkerIn(t *testing.T, in func(checkout string) string) (w, lead *fixture, tree string) {
	t.Helper()
	gittest.Isolate(t)
	lead = newFixture(t, false)
	file := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(file, []byte(workerDoc), 0o644)
	pl, err := plan.Build(file, []byte(workerDoc), plan.Suggest([]byte(workerDoc)))
	if err != nil {
		t.Fatal(err)
	}
	lead.store.SavePlan(pl)
	lead.store.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, lead.now, state.Origin{})
		st.Phases["P0"].Status = state.PhaseDone
		st.Current = "P1"
		state.Start(st, lead.now, state.Origin{})
		return state.Assign(st, pl, state.Worker{Phase: "P2", Ref: state.WorkerRef(lead.store.Ref, "P2"), Session: "wsess"}, lead.now)
	})
	lead.store.AppendHandoff("P0", "Overlay done.")
	lead.store.AppendHandoff("P1", "Not for P2.")
	tree = in(filepath.Dir(lead.dir))
	os.MkdirAll(tree, 0o755)
	gittest.Git(t, tree, "init", "-q", "-b", "baton/sess-1/P2")
	p, err := state.OpenProject(lead.dir, lead.clock)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := p.NewWorker(lead.store.Ref, "P2", "wsess", lead.inst, tree)
	if err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P2")
	sub.SavePlan(only)
	sub.Update(func(st *state.State) error { *st = state.AttachWorker(*st, "P2"); return nil })
	w = &fixture{t: t, dir: lead.dir, store: sub, now: lead.now, inst: state.WorkerInstance(lead.inst, "P2"), sid: "wsess",
		vars: map[string]string{"BATON_ROLE": "worker"}}
	return w, lead, sub.Root
}

// set changes the session's run.
func (f *fixture) set(fn func(st *state.State)) {
	f.t.Helper()
	if _, err := f.store.Update(func(st *state.State) error { fn(st); return nil }); err != nil {
		f.t.Fatal(err)
	}
}

// relays lists the lead's relay queue as phase/kind/decision.
func relays(lead *fixture) string {
	var out []string
	for _, r := range lead.state().Relay {
		out = append(out, strings.TrimSuffix(r.Phase+"/"+r.Kind+"/"+r.Decision, "/"))
	}
	return strings.Join(out, " ")
}

func edit(tool, path string) map[string]any {
	key := "file_path"
	if tool == "NotebookEdit" {
		key = "notebook_path"
	}
	return map[string]any{"tool_name": tool, "tool_input": map[string]any{key: path, "old_string": "a", "new_string": "b", "content": "x"}}
}

// A worker's session starts its phase from a brief of its own, which names its worktree and branch, and
// quotes the notes of the phases its phase builds on. The lead's run is not touched.
func TestAWorkerStartsItsPhaseFromItsBrief(t *testing.T) {
	w, lead, tree := newWorker(t)
	out := w.fire("SessionStart", map[string]any{"source": "startup"})
	brief := additionalContext(out)
	for _, want := range []string{
		`You are a worker for the plan "Demo"`, "this session works on P2 alone, in a git worktree of its own (" + tree + ", on the branch baton/sess-1/P2)",
		"Now: P2 — Third. Begin it now.", "Be kind.", "Do c.", "Notes left by the phases P2 builds on", "Overlay done.",
		"baton then lands your work on the run's branch", "baton puts it to them through the lead session",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	for _, not := range []string{"Do b.", "Not for P2.", filepath.Dir(w.dir), "/baton pause"} {
		if strings.Contains(brief, not) {
			t.Errorf("the brief has %q:\n%s", not, brief)
		}
	}
	if say(out) != "baton: worker on P2 (Third) — it starts now, with its brief" {
		t.Errorf("announced %q", say(out))
	}
	if ps := w.state().Phases["P2"]; ps.Status != state.PhaseActive || !ps.StartedAt.Equal(w.now) {
		t.Errorf("P2 in the worker's run: %+v", ps)
	}
	if !w.logged("phase_started", map[string]any{"phase": "P2", "worker": "P2", "session": "wsess"}) || !w.logged("brief", map[string]any{"type": "worker", "phase": "P2"}) {
		t.Errorf("the worker's events:\n%s", w.eventLog())
	}
	if ps := lead.state().Phases["P2"]; ps.Status != state.PhaseWorking || strings.Contains(lead.eventLog(), "brief") {
		t.Errorf("the lead's run was touched: P2 %+v\n%s", ps, lead.eventLog())
	}

	// Started anew, it gets its brief again; its phase is under way already.
	w.now = w.now.Add(time.Hour)
	if b := additionalContext(w.fire("SessionStart", map[string]any{"source": "startup"})); !strings.Contains(b, "Now: P2 — Third") {
		t.Errorf("started anew: %s", b)
	}
	if ps := w.state().Phases["P2"]; !ps.StartedAt.Equal(lead.now) || strings.Count(w.eventLog(), "phase_started") != 1 {
		t.Errorf("started twice: %+v", ps)
	}
	// Resumed, it is reminded what it is.
	if p := additionalContext(w.fire("SessionStart", map[string]any{"source": "resume"})); !strings.Contains(p, `as a worker for the attached plan "Demo": it works on P2 alone`) ||
		!strings.Contains(p, "Current phase: P2 — Third") || strings.Contains(p, "phase by phase") {
		t.Errorf("resumed: %s", p)
	}
}

// After a compaction, a worker is reminded what it is, and given the notes its phase builds on again.
func TestAWorkersBriefAfterACheckpoint(t *testing.T) {
	w, _, tree := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	w.set(func(st *state.State) { st.CheckpointOwed = true })
	w.fire("Stop", map[string]any{})
	w.set(func(st *state.State) { st.Run.Compaction.Status = state.CompactTyped })
	w.fire("PreCompact", map[string]any{"trigger": "manual"})
	brief := additionalContext(w.fire("SessionStart", map[string]any{"source": "compact"}))
	for _, want := range []string{"compacted at your checkpoint", `You are a worker on P2 of the plan "Demo"`, "in a git worktree of your own (" + tree + ")",
		"Overlay done.", "baton then lands your work on the run's branch"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "Not for P2.") || strings.Contains(brief, "You are running the plan") {
		t.Errorf("the brief reads as the lead's:\n%s", brief)
	}
}

// A worker never changes the lead's checkout, nor plans; its own worktree is its to change.
func TestAWorkerKeepsToItsOwnWorktree(t *testing.T) {
	w, lead, tree := newWorker(t)
	checkout := filepath.Dir(w.dir)
	relative := edit("Edit", "src/a.go")
	relative["cwd"] = tree
	for _, c := range []struct {
		name string
		in   map[string]any
		deny string         // in the refusal ("" for none)
		ev   map[string]any // the worker_refused event
	}{
		{"an edit in the lead's checkout", edit("Edit", filepath.Join(checkout, "src", "a.go")), "edit " + filepath.Join(tree, "src", "a.go") + " instead",
			map[string]any{"tool": "Edit", "why": "the lead's checkout", "path": "src/a.go"}},
		{"a new file there", edit("Write", filepath.Join(checkout, "notes.md")), "edit " + filepath.Join(tree, "notes.md") + " instead", map[string]any{"path": "notes.md"}},
		{"a notebook there", edit("NotebookEdit", filepath.Join(checkout, "n.ipynb")), "lead session's checkout", map[string]any{"path": "n.ipynb"}},
		{"a subagent's edit there", byAgent(edit("Edit", filepath.Join(checkout, "b.go")), "sub-1"), "lead session's checkout", map[string]any{"path": "b.go"}},
		{"entering plan mode", map[string]any{"tool_name": "EnterPlanMode", "tool_input": map[string]any{}}, "its phase is planned already",
			map[string]any{"tool": "EnterPlanMode", "why": "plan mode"}},
		{"an edit in its worktree", edit("Edit", filepath.Join(tree, "src", "a.go")), "", nil},
		{"a relative edit, from its worktree", relative, "", nil},
		{"reading the checkout", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(checkout, "a.go")}}, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := w.fire("PreToolUse", c.in)
			got := denyReason(out)
			if c.deny == "" {
				if got != "" {
					t.Fatalf("refused: %q", got)
				}
				return
			}
			if !strings.Contains(got, c.deny) || strings.Contains(got, checkout+string(filepath.Separator)) {
				t.Errorf("refused with %q, want %q and no path in the checkout", got, c.deny)
			}
			if !strings.Contains(say(out), "baton: ") || !w.logged("worker_refused", c.ev) {
				t.Errorf("said %q; events:\n%s", say(out), w.eventLog())
			}
		})
	}
	// A worktree inside the checkout (baton's home there) is still the worker's to change.
	w, _, tree = newWorkerIn(t, func(checkout string) string { return filepath.Join(checkout, ".home", "worktrees", "P2") })
	if d := denyReason(w.fire("PreToolUse", edit("Edit", filepath.Join(tree, "a.go")))); d != "" {
		t.Errorf("refused an edit in a worktree inside the checkout: %q", d)
	}
	if d := denyReason(w.fire("PreToolUse", edit("Edit", filepath.Join(filepath.Dir(w.dir), "a.go")))); d == "" {
		t.Error("an edit in the checkout, beside the worktree, went through")
	}
	// The lead edits its checkout, and plans, as ever.
	for _, in := range []map[string]any{edit("Edit", filepath.Join(checkout, "src", "a.go")), {"tool_name": "EnterPlanMode", "tool_input": map[string]any{}}} {
		if d := denyReason(lead.fire("PreToolUse", in)); d != "" {
			t.Errorf("the lead was refused %v: %q", in["tool_name"], d)
		}
	}
}

// A worker's stop leaves its questions to the lead: a block, a review or its proposal is queued in the run
// (once, however often it stops) for the lead session to put to the human, and the stop goes through. Its
// phase done, its work is the run's to land.
func TestAWorkersStopLeavesItsQuestionsToTheLead(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(st *state.State, now time.Time)
		say     string // in the announcement
		refused string // in the refusal ("" for a stop let through)
		relays  string // the run's relay queue after two stops
		detail  string // what its question carries: what|because|count
		event   map[string]any
	}{
		{"its phase done", func(st *state.State, _ time.Time) { st.Mode, st.Current = state.ModeComplete, "" },
			"baton: P2 (Third) done → baton lands its work on the run's branch", "", "", "", map[string]any{"kind": "worker_finished", "phase": "P2"}},
		{"blocked", func(st *state.State, now time.Time) { state.SetBlocked(st, "need a key", now) },
			"baton: waiting on the human — blocked on P2: need a key; the lead session asks them", "", "P2/blocked", "|need a key|0", map[string]any{"kind": "escalated", "type": "blocked", "relayed": true}},
		{"a review due", func(st *state.State, now time.Time) {
			state.AddNote(st, "used a fake", "", now)
			state.DueReview(st, "P2", now)
		},
			"baton: waiting on the human — review the decisions P2 made without you; the lead session asks them", "", "P2/review", "||1", map[string]any{"kind": "escalated", "type": "review"}},
		{"its proposal waits", func(st *state.State, now time.Time) {
			state.AddProposal(st, state.Decision{What: "use a fake", Because: "no clock", Relayed: true}, now)
		},
			"baton: P2 (Third) waits on the human's answer to proposal P2.d1, which the lead session puts to them", "", "P2/proposal/P2.d1", "use a fake|no clock|0",
			map[string]any{"kind": "idle_on_relay", "id": "P2.d1"}},
		{"no status", func(*state.State, time.Time) {}, "", "baton puts it to the human through the lead session", "", "", map[string]any{"kind": "stop_refused", "phase": "P2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead, _ := newWorker(t)
			w.fire("SessionStart", map[string]any{"source": "startup"})
			w.set(func(st *state.State) { c.set(st, w.now) })
			out := w.fire("Stop", map[string]any{})
			if c.refused == "" && out["decision"] == "block" || !strings.Contains(say(out), c.say) || c.refused != "" && !strings.Contains(feedback(out), c.refused) {
				t.Fatalf("stop: %v", out)
			}
			if c.refused == "" {
				w.fire("Stop", map[string]any{})
			}
			if got := relays(lead); got != c.relays {
				t.Errorf("the run's relay queue: %q, want %q", got, c.relays)
			}
			kind := c.event["kind"].(string)
			if !w.logged(kind, c.event) {
				t.Errorf("no %v among:\n%s", c.event, w.eventLog())
			}
			if c.relays != "" && !lead.logged("relay_queued", map[string]any{"phase": "P2"}) {
				t.Errorf("the run's events:\n%s", lead.eventLog())
			}
			if c.relays != "" {
				// The question carries what the lead needs to ask it, and the human is told it is coming.
				r, ann := lead.state().Relay[0], lead.state().Run.Announce
				if got := fmt.Sprint(r.What, "|", r.Because, "|", r.Count); got != c.detail {
					t.Errorf("queued with %q, want %q", got, c.detail)
				}
				if len(ann) != 1 || !strings.Contains(ann[0], "baton: the worker on P2 has a question for you") {
					t.Errorf("announced %q", ann)
				}
			}
			st := w.state()
			if n := len(st.Run.Notices); c.event["kind"] == "escalated" && (n != 1 || st.Run.Escalation == nil || !st.Run.Escalation.Asked || st.Run.Escalation.Question != "") {
				t.Errorf("the human is pushed once and asked nothing in the worker: %d notices, %+v", n, st.Run.Escalation)
			}
			for _, n := range st.Run.Notices {
				if n.Kind == "plan_complete" {
					t.Errorf("a worker's phase done is not a plan complete: %+v", n)
				}
			}
		})
	}
}

// The plan file is the lead's to follow: a worker's stop neither adopts a change to it nor pauses for one,
// and a worker never restarts on a newer baton.
func TestAWorkerLeavesThePlanFileToTheLead(t *testing.T) {
	w, lead, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	pl, _ := lead.store.LoadPlan()
	os.WriteFile(pl.File, []byte(strings.Replace(workerDoc, "## P2 — Third", "## P3 — Renamed", 1)), 0o644)
	w.fire("Stop", map[string]any{})
	if st := w.state(); st.Mode != state.ModeRunning || st.PlanDrift != "" || strings.Contains(w.eventLog(), "plan_changed") {
		t.Errorf("the worker followed the plan file: mode %s drift %q\n%s", st.Mode, st.PlanDrift, w.eventLog())
	}
	if only, _ := w.store.LoadPlan(); len(only.Phases) != 1 || only.Phases[0].ID != "P2" {
		t.Errorf("the worker's plan: %+v", only.Phases)
	}
}

// Nobody watches a worker to answer the context question: it is nudged to checkpoint instead.
func TestAWorkerIsNudgedToCheckpointNotAsked(t *testing.T) {
	w, _, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	w.vars["BATON_COMPACT_CAP"] = "810000"
	w.setContext(730_000)
	out := w.fire("PostToolUse", bash("go test ./..."))
	if tell := feedback(out); !strings.Contains(tell, "Checkpoint due") || strings.Contains(tell, "AskUserQuestion") {
		t.Fatalf("told %q", tell)
	}
	if st := w.state(); st.Run.WarnOwed || st.Run.WarnQuestion != "" || !w.logged("context_nudge", nil) || w.logged("context_warning", nil) {
		t.Errorf("warn owed %v, question %q\n%s", st.Run.WarnOwed, st.Run.WarnQuestion, w.eventLog())
	}
}

// A worker's proposal is the lead's to put to the human: meanwhile nothing is held in the worker, and its
// valves still run.
func TestAWorkersProposalHoldsNothingUp(t *testing.T) {
	w, _, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	w.set(func(st *state.State) { state.AddProposal(st, state.Decision{What: "use a fake", Relayed: true}, w.now) })
	if d := denyReason(w.fire("PreToolUse", bash("go test ./..."))); d != "" {
		t.Fatalf("held: %q", d)
	}
	w.vars["BATON_COMPACT_CAP"] = "810000"
	w.setContext(500_000)
	if tell := feedback(w.fire("PostToolUse", bash("go test ./..."))); !strings.Contains(tell, "Checkpoint due") {
		t.Errorf("the checkpoint nudge waited on the lead's question: %q", tell)
	}
}

// Only the lead session attaches plans: one approved in a worker is left alone.
func TestAPlanApprovedInAWorkerIsNotAttached(t *testing.T) {
	w, lead, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	out := w.approve(w.writePlan("other.md", approvedDoc))
	if say(out) != "baton: plan approved in the worker on P2 — not attached: only the lead session attaches plans" ||
		!strings.Contains(feedback(out), "only the lead session attaches plans. Carry on with P2") {
		t.Fatalf("approval: %v", out)
	}
	if !w.logged("plan_not_attached", map[string]any{"why": "worker"}) {
		t.Errorf("events:\n%s", w.eventLog())
	}
	only, _ := w.store.LoadPlan()
	run, _ := lead.store.LoadPlan()
	if st := w.state(); st.Mode != state.ModeRunning || st.Current != "P2" || len(only.Phases) != 1 || len(run.Phases) != 3 {
		t.Errorf("a plan was attached: worker %s on %s, %d phases; the run's %d phases", st.Mode, st.Current, len(only.Phases), len(run.Phases))
	}
}

// The host ends and resumes its workers itself: a worker's session ending is no news for the human.
func TestAWorkersSessionEndingIsNotPushed(t *testing.T) {
	w, _, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	w.fire("SessionEnd", map[string]any{"reason": "other"})
	if st := w.state(); len(st.Run.Notices) != 0 || st.Run.Ended == nil {
		t.Errorf("notices %+v, ended %+v", st.Run.Notices, st.Run.Ended)
	}
}

// The branch checked out when a plan is approved is the run's: workers branch off it.
func TestAnApprovedPlanRecordsTheRunsBranch(t *testing.T) {
	gittest.Isolate(t)
	f := newFixture(t, false)
	gittest.Git(t, filepath.Dir(f.dir), "init", "-q", "-b", "trunk")
	f.approve(f.writePlan("p.md", approvedDoc))
	if st := f.state(); st.RunBranch != "trunk" || !f.logged("attached", map[string]any{"branch": "trunk"}) {
		t.Errorf("run branch %q\n%s", st.RunBranch, f.eventLog())
	}
}

// Every new path of a worker's hooks fails open: a panic in one exits 0 and writes nothing, and so does a
// relay that cannot reach the run.
func TestAWorkersHooksFailOpen(t *testing.T) {
	checkout := func(w *fixture) string { return filepath.Dir(w.dir) }
	for _, c := range []struct {
		name  string
		event string
		in    func(w *fixture) map[string]any
		set   func(st *state.State, now time.Time)
	}{
		{"its start", "SessionStart", func(*fixture) map[string]any { return map[string]any{"source": "startup"} }, nil},
		{"its edit refused", "PreToolUse", func(w *fixture) map[string]any { return edit("Edit", filepath.Join(checkout(w), "a.go")) }, nil},
		{"plan mode refused", "PreToolUse", func(*fixture) map[string]any {
			return map[string]any{"tool_name": "EnterPlanMode", "tool_input": map[string]any{}}
		}, nil},
		{"a plan approved", "PostToolUse", func(w *fixture) map[string]any {
			return map[string]any{"tool_name": "ExitPlanMode", "tool_response": map[string]any{"filePath": w.writePlan("x.md", approvedDoc)}}
		}, nil},
		{"its block", "Stop", func(*fixture) map[string]any { return map[string]any{} }, func(st *state.State, now time.Time) { state.SetBlocked(st, "x", now) }},
		{"its review", "Stop", func(*fixture) map[string]any { return map[string]any{} }, func(st *state.State, now time.Time) { state.DueReview(st, "P2", now) }},
		{"its proposal", "Stop", func(*fixture) map[string]any { return map[string]any{} }, func(st *state.State, now time.Time) {
			state.AddProposal(st, state.Decision{What: "x", Relayed: true}, now)
		}},
		{"its phase done", "Stop", func(*fixture) map[string]any { return map[string]any{} }, func(st *state.State, _ time.Time) { st.Mode = state.ModeComplete }},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, how := range []string{"panic", "unreachable run"} {
				w, lead, _ := newWorker(t)
				if c.event != "SessionStart" {
					w.fire("SessionStart", map[string]any{"source": "startup"})
				}
				if c.set != nil {
					w.set(func(st *state.State) { c.set(st, w.now) })
				}
				open := OpenRun(w.clock)
				if how == "panic" {
					open = func(ctx Context) (*state.Store, error) {
						s, err := OpenRun(w.clock)(ctx)
						if s != nil {
							s.Now = func() time.Time { panic("boom") }
						}
						return s, err
					}
				} else {
					os.WriteFile(filepath.Join(lead.store.Dir, "state.json"), []byte("{not json"), 0o644)
				}
				in := c.in(w)
				in["hook_event_name"], in["session_id"] = c.event, w.sid
				raw, _ := json.Marshal(in)
				var out, errb bytes.Buffer
				code := Dispatch(c.event, bytes.NewReader(raw), &out, &errb, w.env(w.inst), w.clock, Handlers(Deps{Open: open}))
				if code != 0 || how == "panic" && (out.Len() != 0 || !strings.Contains(errb.String(), "failed open")) {
					t.Errorf("%s: exit %d, stdout %q, stderr %q", how, code, out.String(), errb.String())
				}
			}
		})
	}
}

// The decisions a worker makes without the human are the run's: the lead's model has them at session
// start, is handed each once as the human writes to it (marked told in the worker's sub-run), and the
// plan-complete notice counts them.
func TestAWorkersDecisionsReachTheLead(t *testing.T) {
	w, lead, _ := newWorker(t)
	w.set(func(st *state.State) {
		state.Start(st, w.now, state.Origin{})
		if _, err := state.AddNote(st, "used a fake clock", "revert the commit", w.now); err != nil {
			t.Fatal(err)
		}
	})
	lead.now = lead.now.Add(time.Minute)
	lead.set(func(st *state.State) {
		if _, err := state.AddNote(st, "skipped the flaky test", "run it again", lead.now); err != nil {
			t.Fatal(err)
		}
	})
	ctx := additionalContext(lead.fire("SessionStart", map[string]any{"source": "clear"}))
	if !strings.Contains(ctx, "- P2.d1 (P2, ") || !strings.Contains(ctx, "noted: used a fake clock. Undo: revert the commit") ||
		strings.Index(ctx, "P2.d1") > strings.Index(ctx, "- d1 (P1, ") {
		t.Errorf("the lead's primer:\n%s", ctx)
	}

	out := lead.fire("UserPromptSubmit", map[string]any{"prompt": "undo the clock thing"})
	if ctx := additionalContext(out); !strings.Contains(ctx, "P2.d1") || !strings.Contains(ctx, "d1 (P1") {
		t.Errorf("not handed over:\n%s", ctx)
	}
	if msg, _ := out["systemMessage"].(string); !strings.Contains(msg, "decisions made without you since you last wrote: P2.d1, d1") {
		t.Errorf("said %q", msg)
	}
	if d := w.state().Decisions; len(d) != 1 || !d[0].Told {
		t.Errorf("the worker's decision is not marked told: %+v", d)
	}
	if ctx := additionalContext(lead.fire("UserPromptSubmit", map[string]any{"prompt": "thanks"})); strings.Contains(ctx, "P2.d1") {
		t.Errorf("handed over twice:\n%s", ctx)
	}

	// The worker's own session is handed its own, and nothing of the lead's.
	w.set(func(st *state.State) {
		state.AddNote(st, "pinned the version", "unpin it", w.now)
	})
	if ctx := additionalContext(w.fire("UserPromptSubmit", map[string]any{"prompt": "what did you do?"})); !strings.Contains(ctx, "P2.d2") || strings.Contains(ctx, "skipped the flaky test") {
		t.Errorf("the worker was handed:\n%s", ctx)
	}

	lead.fire("PreCompact", map[string]any{"trigger": "auto"})
	if ctx := additionalContext(lead.fire("SessionStart", map[string]any{"source": "compact"})); !strings.Contains(ctx, "- P2.d1 (P2, ") {
		t.Errorf("the lead's brief after a compaction:\n%s", ctx)
	}

	st := lead.state()
	st.Mode = state.ModeComplete
	pl, _ := lead.store.LoadPlan()
	d := decideStop(&st, pl, lead.now, decide.Words{}, lead.store.WorkerDecisions(st))
	if msg, _ := d.output["systemMessage"].(string); msg != "baton: plan complete — all 3 phases done · 3 decisions made without you — /baton status" {
		t.Errorf("said %q", msg)
	}
}
