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

// A plan the human approves is kept as they approved it: every replan of it is compared with that text.
func TestAnApprovedPlanIsKeptForItsReplans(t *testing.T) {
	f := newFixture(t, false)
	file := f.writePlan("humming-whistling-hare.md", approvedDoc)
	f.approve(file)
	if b, err := os.ReadFile(f.store.ReplanText(0)); err != nil || string(b) != approvedDoc {
		t.Fatalf("the approved text: %q, %v", b, err)
	}
	if !f.logged("plan_kept", map[string]any{"plan": file}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
}

// While a replan is open, the plan file is the replan's: the Stop hook leaves a change to it alone.
func TestTheStopLeavesThePlanFileToAnOpenReplan(t *testing.T) {
	f := newFixture(t, true)
	pl, _ := f.store.LoadPlan()
	f.set(func(st *state.State) { st.Replan = &state.Replan{By: state.ReplanHuman, Since: f.now, N: 1} })
	doc, _ := os.ReadFile(pl.File)
	os.WriteFile(pl.File, append(doc, []byte("\n## P2 — Third\nDo c.\n")...), 0o644)
	f.fire("Stop", map[string]any{})
	if after, _ := f.store.LoadPlan(); len(after.Phases) != 2 || f.logged("plan_changed", nil) {
		t.Errorf("the change was followed while the replan was open: %+v\n%s", after.Phases, f.eventLog())
	}
	f.set(func(st *state.State) { st.Replan = nil })
	f.fire("Stop", map[string]any{})
	if after, _ := f.store.LoadPlan(); len(after.Phases) != 3 || !f.logged("plan_changed", map[string]any{"adopted": true}) {
		t.Errorf("the change was not followed once the replan closed: %+v", after.Phases)
	}
}

// replanned leaves f's run as a replan that added P1b would: the plan file its draft, plan.json and the
// run following it, and the text it found kept as replan/1.md. It returns that text.
func (f *fixture) replanned() []byte {
	f.t.Helper()
	pl, _ := f.store.LoadPlan()
	doc, _ := os.ReadFile(pl.File)
	draft := append(append([]byte{}, doc...), []byte("\n## P1b — Extra\nDo x.\n")...)
	next, err := plan.Build(pl.File, draft, plan.Suggest(draft))
	if err != nil {
		f.t.Fatal(err)
	}
	f.store.WriteReplanText(1, doc, true)
	os.WriteFile(pl.File, draft, 0o644)
	f.store.SavePlan(next)
	f.set(func(st *state.State) {
		state.AdoptReplan(st, next, f.now)
		st.Replans = []state.Replanned{{N: 1, By: state.ReplanHuman, At: f.now}}
	})
	return doc
}

// A replan's kept text copied back over the plan file undoes it: the Stop hook follows it, the phase the
// replan added leaves the run, and the human is told.
func TestTheStopFollowsAReplanUndone(t *testing.T) {
	f := newFixture(t, true)
	doc := f.replanned()
	pl, _ := f.store.LoadPlan()
	os.WriteFile(pl.File, doc, 0o644) // the undo
	out := f.fire("Stop", map[string]any{})
	if got := say(out); !strings.Contains(got, "baton: the plan file is back to the text replan 1 started from → baton follows it: P1b dropped") {
		t.Errorf("said %q", got)
	}
	st := f.state()
	if after, _ := f.store.LoadPlan(); after.Index("P1b") >= 0 || st.Phases["P1b"] != nil || st.Replans[0].Undone.IsZero() || after.SHA() != after.SHA256 {
		t.Errorf("plan %+v, phases %v, replans %+v", after.Phases, st.Phases, st.Replans)
	}
	if !f.logged("replan_undone", map[string]any{"n": float64(1)}) || f.logged("plan_changed", nil) {
		t.Errorf("events:\n%s", f.eventLog())
	}
}

// An undo that would drop a phase started since is not followed: the run goes on with the plan as it is,
// and says why, once.
func TestTheStopReportsAnUndoItCannotFollow(t *testing.T) {
	f := newFixture(t, true)
	doc := f.replanned()
	f.set(func(st *state.State) {
		st.Phases["P1b"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "sess-1/P1b"}
	})
	pl, _ := f.store.LoadPlan()
	os.WriteFile(pl.File, doc, 0o644)
	out := f.fire("Stop", map[string]any{})
	if got := say(out); !strings.Contains(got, "baton cannot follow it (P1b started since, and the text copied back lacks it)") {
		t.Errorf("said %q", got)
	}
	if after, _ := f.store.LoadPlan(); after.Index("P1b") < 0 || !f.logged("plan_changed", map[string]any{"adopted": false}) {
		t.Errorf("plan %+v; events:\n%s", after.Phases, f.eventLog())
	}
}

// Following an undo at the stop fails open: a panic in it exits 0 and writes nothing.
func TestAnUndoAtTheStopFailsOpen(t *testing.T) {
	f := newFixture(t, true)
	doc := f.replanned()
	pl, _ := f.store.LoadPlan()
	os.WriteFile(pl.File, doc, 0o644)
	open := func(ctx Context) (*state.Store, error) {
		s, err := OpenRun(f.clock)(ctx)
		if s != nil {
			s.Now = func() time.Time { panic("boom") }
		}
		return s, err
	}
	in := map[string]any{"hook_event_name": "Stop", "session_id": f.sid, "transcript_path": filepath.Join(t.TempDir(), "t.jsonl")}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("Stop", bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
	if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}
