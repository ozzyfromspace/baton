package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// planMode writes doc as the session's plan-mode file, <plans>/<slug>.md, in a plans folder of its own,
// and has the session's transcript carry slug, as Claude Code writes them. It returns the file.
func (f *fixture) planMode(slug, doc string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	file := filepath.Join(dir, slug+".md")
	os.WriteFile(file, []byte(doc), 0o644)
	f.slugged(slug)
	return file
}

// slugged appends to the session's transcript an entry of its own conversation carrying plan slug slug.
func (f *fixture) slugged(slug string) {
	f.t.Helper()
	tf, err := os.OpenFile(f.leadTranscript(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tf.Close()
	fmt.Fprintf(tf, `{"type":"attachment","isSidechain":false,"attachment":{"type":"plan_mode"},"uuid":"pm-%s","timestamp":"2026-10-04T11:59:00Z","slug":%q}`+"\n", slug, slug)
}

// planQuestion is baton's question about the plan the human approved mid-run, as the model puts it.
func (f *fixture) planQuestion() map[string]any {
	q := planQuestionOf(f.state().PlanQuestion)
	var labels []string
	for _, o := range q.Options {
		labels = append(labels, o.Label)
	}
	return ask(q.Text, labels...)
}

// midRun leaves the fixture's run with P0 done and P1 under way.
func (f *fixture) midRun() {
	f.store.Update(func(st *state.State) error {
		st.Phases["P0"].Status, st.Phases["P1"].Status = state.PhaseDone, state.PhaseActive
		st.Current, st.BoundaryOwed = "P1", false
		st.Run.Compaction = state.Compaction{Epoch: st.Run.Compaction.Epoch}
		return nil
	})
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s is still there (%v): Claude Code would add it to the context after every compaction", path, err)
	}
}

// A plan approved in plan mode is the session's plan-mode file: baton moves it out of Claude Code's way as
// it attaches it, says so, and runs it from there.
func TestAnApprovedPlanModeFileMovesOutOfClaudeCodesWay(t *testing.T) {
	f := newFixture(t, false)
	file := f.planMode("humming-whistling-hare", approvedDoc)
	out := f.approve(file)
	moved := filepath.Join(filepath.Dir(file), "the-next-campaign.md")
	pl, _ := f.store.LoadPlan()
	if pl.File != moved || pl.Origin != file || pl.SHA256 != pl.SHA() {
		t.Fatalf("plan %+v", pl)
	}
	gone(t, file)
	if b, _ := os.ReadFile(moved); string(b) != approvedDoc {
		t.Fatalf("moved text %q", b)
	}
	if got := say(out); !strings.Contains(got, `attached "The next campaign"`) || !strings.Contains(got, decide.PlanMoved(file, moved)) {
		t.Fatalf("announced %q", got)
	}
	if tell := feedback(out); !strings.Contains(tell, moved) {
		t.Fatalf("the model is not told where the plan is: %q", tell)
	}
	if !f.logged("attached", map[string]any{"plan": moved, "by": "approval"}) || !f.logged("plan_moved", map[string]any{"from": file, "to": moved, "by": "approval"}) {
		t.Fatalf("events: %s", f.eventLog())
	}
	if !f.state().PlanLooked {
		t.Error("the move is not on record as looked at: the boundary would look again")
	}
	if b, err := os.ReadFile(f.store.ReplanText(0)); err != nil || string(b) != approvedDoc {
		t.Errorf("the approved text kept for replans: %q, %v", b, err)
	}
	f.fire("Stop", map[string]any{})
	f.store.Update(func(st *state.State) error { st.Run.Compaction.Status = state.CompactTyped; return nil })
	if _, brief := f.compacted("manual"); !strings.Contains(brief, moved) || strings.Contains(brief, file) {
		t.Fatalf("the brief does not name the plan where it is:\n%s", brief)
	}
}

// The run follows the plan under its new name: an edit there is followed, and the plan-mode file Claude
// Code writes afresh at the next planning is no change of the run's plan.
func TestAMovedPlanIsFollowedUnderItsNewName(t *testing.T) {
	f := newFixture(t, false)
	file := f.planMode("humming-whistling-hare", approvedDoc)
	f.approve(file)
	f.midRun()
	pl, _ := f.store.LoadPlan()
	os.WriteFile(file, []byte("# Something else\n\n## P0 — Other\nx\n"), 0o644)
	if msg := say(f.fire("Stop", map[string]any{})); strings.Contains(msg, "plan file changed") || f.logged("plan_changed", nil) {
		t.Fatalf("the plan-mode file counted as the run's: %q", msg)
	}
	os.WriteFile(pl.File, []byte(strings.Replace(approvedDoc, "Then grow it.", "Then grow it, slowly.", 1)), 0o644)
	if msg := say(f.fire("Stop", map[string]any{})); !strings.Contains(msg, "the plan file changed; every phase still to run is there") {
		t.Fatalf("an edit of the moved plan: %q", msg)
	}
	if after, _ := f.store.LoadPlan(); after.SHA256 != after.SHA() || after.File != pl.File || after.Origin != file ||
		!f.logged("plan_changed", map[string]any{"plan": pl.File, "adopted": true}) {
		t.Fatalf("not followed: %+v; events: %s", after, f.eventLog())
	}
}

// A plan file that is not the session's plan-mode file stays where it is: one of another slug, and any
// when the session's transcript names no slug.
func TestAPlanFileNotTheSessionsStays(t *testing.T) {
	for _, slug := range []string{"another-session-slug", ""} {
		f := newFixture(t, false)
		file := f.planMode("humming-whistling-hare", approvedDoc)
		f.tp = filepath.Join(t.TempDir(), "transcript.jsonl")
		os.WriteFile(f.tp, []byte(`{"type":"user","promptSource":"typed","uuid":"u0","timestamp":"2026-10-04T11:00:00Z"}`+"\n"), 0o644)
		if slug != "" {
			f.slugged(slug)
		}
		f.approve(file)
		if pl, _ := f.store.LoadPlan(); pl.File != file || pl.Origin != "" || len(pl.Phases) != 2 {
			t.Fatalf("slug %q: plan %+v", slug, pl)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("slug %q: %v", slug, err)
		}
		if f.logged("plan_moved", nil) || f.logged("plan_not_moved", nil) {
			t.Errorf("slug %q: events: %s", slug, f.eventLog())
		}
	}
}

// A plan-mode file another live run's plan is stays where it is, for that run's sake.
func TestAPlanFileAnotherLiveRunUsesStays(t *testing.T) {
	f := newFixture(t, false)
	file := f.planMode("humming-whistling-hare", approvedDoc)
	p, err := state.OpenProject(f.dir, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"})
	if err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build(file, []byte(approvedDoc), plan.Suggest([]byte(approvedDoc)))
	if err != nil {
		t.Fatal(err)
	}
	other.SavePlan(pl)
	other.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, f.now, state.Origin{})
		return state.Claim(st, "inst-2", 4242, f.now)
	})
	f.approve(file)
	if got, _ := f.store.LoadPlan(); got.File != file || got.Origin != "" {
		t.Fatalf("plan %+v", got)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if !f.logged("plan_not_moved", map[string]any{"plan": file, "why": "another live run's plan", "run": other.Ref, "by": "approval"}) || f.logged("plan_moved", nil) {
		t.Errorf("events: %s", f.eventLog())
	}

	// Once that run's host is gone, it is no one's plan but this session's: the next plan approved moves.
	other.Update(func(st *state.State) error { st.Owner.Heartbeat = f.now.Add(-time.Hour); return nil })
	f.store.Update(func(st *state.State) error { st.Mode = state.ModeComplete; return nil })
	f.approve(file)
	if got, _ := f.store.LoadPlan(); got.Origin != file {
		t.Errorf("plan %+v", got)
	}
}

// Claude Code writes the session's next plan to its plan-mode file again: approved mid-run, it is a
// revision of the run's plan, which baton moves out of the way in its turn as it is approved, and the run
// goes on with it under its new name, keeping the file it came from as its origin.
func TestARevisionApprovedAtThePlansOriginRevisesTheRun(t *testing.T) {
	f := newFixture(t, false)
	file := f.planMode("humming-whistling-hare", approvedDoc)
	f.approve(file)
	f.midRun()
	first := filepath.Join(filepath.Dir(file), "the-next-campaign.md")
	revised := strings.Replace(approvedDoc, "## Verification", "## P2 — Polish\nShine.\n\n## Verification", 1)
	os.WriteFile(file, []byte(revised), 0o644)
	out := f.approve(file)
	second := filepath.Join(filepath.Dir(file), "the-next-campaign-2.md")
	if !strings.Contains(feedback(out), "you approved a revised plan") || !strings.Contains(say(out), decide.PlanMoved(file, second)) {
		t.Fatalf("asked %q, said %q", feedback(out), say(out))
	}
	if q := f.state().PlanQuestion; q == nil || !q.Same || q.File != second || q.Origin != file {
		t.Fatalf("question %+v", q)
	}
	gone(t, file)
	if b, _ := os.ReadFile(first); string(b) != approvedDoc {
		t.Fatalf("the run's plan file changed under it: %q", b)
	}
	if !f.logged("plan_moved", map[string]any{"from": file, "to": second, "by": "revision"}) || !f.logged("plan_question_asked", map[string]any{"plan": second, "same": true}) {
		t.Fatalf("events: %s", f.eventLog())
	}
	question := f.planQuestion()
	f.fire("PermissionRequest", question)
	f.fire("PostToolUse", answer(question, "Continue with the revised plan"))
	pl, _ := f.store.LoadPlan()
	if st := f.state(); pl.File != second || pl.Origin != file || len(pl.Phases) != 3 || st.Current != "P1" || st.PlanQuestion != nil {
		t.Fatalf("after revising: %+v, %+v", pl, st)
	}

	// The origin stays the plan's: the next plan made in the session is a revision again.
	os.WriteFile(file, []byte(revised), 0o644)
	f.approve(file)
	if q := f.state().PlanQuestion; q == nil || !q.Same {
		t.Fatalf("question %+v", q)
	}
}

// A new plan approved mid-run from the session's plan-mode file and started over from P0 moves out of
// Claude Code's way as it is attached.
func TestANewPlanStartedOverMovesAside(t *testing.T) {
	f := newFixture(t, false)
	f.startRun() // a plan not from plan mode: it stays where it is
	file := f.planMode("calm-river-otter", approvedDoc)
	f.approve(file)
	if q := f.state().PlanQuestion; q == nil || q.Same || q.File != file {
		t.Fatalf("question %+v", q)
	}
	question := f.planQuestion()
	f.fire("PermissionRequest", question)
	out := f.fire("PostToolUse", answer(question, "Start the new plan from P0"))
	moved := filepath.Join(filepath.Dir(file), "the-next-campaign.md")
	if pl, _ := f.store.LoadPlan(); pl.File != moved || pl.Origin != file || f.state().Current != "P0" {
		t.Fatalf("plan %+v", pl)
	}
	gone(t, file)
	if !strings.Contains(say(out), decide.PlanMoved(file, moved)) || !f.logged("plan_moved", map[string]any{"from": file, "to": moved}) {
		t.Fatalf("said %q; events: %s", say(out), f.eventLog())
	}
}

// A panic on the way to moving the plan fails open: the approval exits 0, writes nothing, and the plan-mode
// file stays where it is.
func TestMovingThePlanFailsOpen(t *testing.T) {
	f := newFixture(t, false)
	file := f.planMode("humming-whistling-hare", approvedDoc)
	open := func(ctx Context) (*state.Store, error) {
		s, err := OpenRun(f.clock)(ctx)
		if s != nil {
			s.Now = func() time.Time { panic("boom") }
		}
		return s, err
	}
	in := map[string]any{"hook_event_name": "PostToolUse", "session_id": f.sid, "transcript_path": f.leadTranscript(), "tool_name": "ExitPlanMode",
		"tool_input": map[string]any{}, "tool_response": map[string]any{"plan": "…", "isAgent": false, "filePath": file}}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	code := Dispatch("PostToolUse", bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
	if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the plan-mode file: %v", err)
	}
}
