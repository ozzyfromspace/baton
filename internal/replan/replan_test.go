package replan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// chainDoc is a plan of six phases in one chain, its last a QA phase.
const chainDoc = `# Chains

Why this plan exists.

## Standing rules

- Be careful.

## P0 — Schema

Build the schema.

## P1 — API

Build the API.

## P2 — CLI

Build the CLI.

## P3 — Docs

Write the docs.

## P4 — Web

Build the web app.

## P5 — QA

Type: qa

Run everything.

## Verification

All green.
`

// widened is chainDoc with the docs and the web app after the API alone, and the QA phase after all: what
// a replan by the shape rules makes of it.
var widened = strings.NewReplacer(
	"Write the docs.", "After: P1\n\nWrite the docs.",
	"Build the web app.", "After: P1\n\nBuild the web app.",
	"Type: qa", "After: all\nType: qa",
).Replace(chainDoc)

type rig struct {
	t    *testing.T
	now  time.Time
	s    *state.Store
	file string
}

// newRig is a run of chainDoc, attached as the human approved it, P0 done and P1 in hand.
func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	r.file = filepath.Join(dir, "plan.md")
	os.WriteFile(r.file, []byte(chainDoc), 0o644)
	var err error
	if r.s, err = state.Open(filepath.Join(dir, ".baton", "runs", "sess-1"), "inst", func() time.Time { return r.now }); err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build(r.file, []byte(chainDoc), plan.Suggest([]byte(chainDoc)))
	if err != nil {
		t.Fatal(err)
	}
	r.s.SavePlan(pl)
	r.s.Update(func(st *state.State) error {
		*st = state.Attach(pl, r.now, state.Origin{})
		state.Done(st, pl, "P0", r.now, state.DoneOpts{})
		state.Start(st, r.now, state.Origin{})
		return nil
	})
	KeepApproved(r.s, pl, "")
	return r
}

func (r *rig) state() state.State {
	st, _ := r.s.Load()
	return st
}

func (r *rig) plan() plan.Plan {
	pl, _ := r.s.LoadPlan()
	return pl
}

func (r *rig) read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func (r *rig) draft(doc string) {
	if err := os.WriteFile(r.s.DraftPath(), []byte(doc), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// event is the last event of kind, or nil.
func (r *rig) event(kind string) map[string]any {
	var last map[string]any
	for _, line := range strings.Split(r.read(filepath.Join(r.s.Dir, "events.jsonl")), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["kind"] == kind {
			last = ev
		}
	}
	return last
}

func (r *rig) open(words string) Opened {
	r.t.Helper()
	o, err := Open(r.s, state.ReplanHuman, words, r.now, Max)
	if err != nil {
		r.t.Fatal(err)
	}
	return o
}

// A human-run replan of a chain: baton opens it (the draft, the text it found, the brief), checks the
// model's draft, writes it over the plan file and follows it, and records it as a decision with its undo.
func TestAHumanReplanOfAChainIsCheckedWrittenFollowedAndRecorded(t *testing.T) {
	r := newRig(t)
	o := r.open("")
	for _, want := range []string{
		`Replan the phases of "Chains" that have not started`,
		"The human gave nothing in particular to consider",
		"  P2 CLI — after P1\n  P3 Docs — after P2\n",
		"  P0 Schema — done\n  P1 API — in hand\n",
		"The phases not started now: 4 phases · longest chain 4 (P2 → P3 → P4 → P5) · one at a time.",
		"1. A dependency is a need.",
		"- split a phase not started",
		"- no phase not started is dropped or merged away",
		"stay byte for byte",
		"Edit only the draft, " + r.s.DraftPath(),
		`baton replanned --notes "<what changed and why>"`,
		`baton replanned --none "<why nothing should change>"`,
		"within 20 minutes",
	} {
		if !strings.Contains(o.Brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, o.Brief)
		}
	}
	st := r.state()
	if rp := st.Replan; rp == nil || rp.By != state.ReplanHuman || rp.N != 1 || !rp.Since.Equal(r.now) || o.Again {
		t.Fatalf("replan %+v", rp)
	}
	if r.read(r.s.DraftPath()) != chainDoc || r.read(r.s.ReplanText(1)) != chainDoc || r.read(r.s.ReplanText(0)) != chainDoc {
		t.Error("the draft, the text the replan found, or the approved text is not the plan file")
	}
	if ev := r.event("replan_opened"); ev == nil || ev["by"] != "human" || ev["n"] != float64(1) || ev["unstarted"] != float64(4) {
		t.Errorf("replan_opened %v", ev)
	}
	if a := st.Run.Announce; len(a) != 1 || !strings.Contains(a[0], "baton: replanning the phases not started (you asked) → no new phase agent starts") {
		t.Errorf("announced %q", a)
	}

	r.now = r.now.Add(5 * time.Minute)
	r.draft(widened)
	f, err := Finish(r.s, "the docs and the web app need only the API", r.now)
	if err != nil {
		t.Fatal(err)
	}
	if r.read(r.file) != widened {
		t.Error("the plan file is not the draft")
	}
	pl := r.plan()
	if d := pl.Deps("P3"); len(d) != 1 || d[0] != "P1" || pl.SHA256 != f.Plan.SHA256 || pl.SHA() != pl.SHA256 {
		t.Errorf("plan.json: P3 after %v, hash %s (file %s)", d, pl.SHA256, pl.SHA())
	}
	st = r.state()
	if st.Replan != nil || st.Current != "P1" || st.Phases["P1"].Status != state.PhaseActive || st.Phases["P0"].Status != state.PhaseDone || f.Took != "" {
		t.Fatalf("after: replan %+v, current %s, took %q", st.Replan, st.Current, f.Took)
	}
	d := st.Decisions[len(st.Decisions)-1]
	wantWhat := "baton replanned the phases not started, at the human's /baton replan: P3 now after P1 (was after P2); P4 now after P1 (was after P3); " +
		"P5 now after P0, P1, P2, P3 and P4 (was after P4) — why: the docs and the web app need only the API"
	if d.ID != f.Decision.ID || d.Kind != state.Note || d.Phase != "" || d.What != wantWhat || d.Undo != decide.ReplanUndo(r.s.ReplanText(1), r.file) {
		t.Errorf("decision %+v", d)
	}
	if !strings.Contains(d.Undo, "1.md back over "+r.file) {
		t.Errorf("undo %q", d.Undo)
	}
	if rs := st.Replans; len(rs) != 1 || rs[0].N != 1 || rs[0].Decision != d.ID || len(rs[0].Changed) != 3 || len(rs[0].Approved) != 3 {
		t.Errorf("replans %+v", rs)
	}
	if a := st.Run.Announce; len(a) != 2 || !strings.HasPrefix(a[1], "baton: replanned → P3 now after P1 (was after P2)") ||
		!strings.Contains(a[1], "not started: 4 phases · longest chain 4 → 2 · up to 1 → 3 at once") {
		t.Errorf("announced %q", a)
	}
	if n := st.Run.Notices; len(n) != 1 || n[0].Kind != "replanned" || !strings.HasPrefix(n[0].Text, "Chains: replanned — P3 now after P1") {
		t.Errorf("notices %+v", n)
	}
	ev := r.event("plan_replanned")
	before, _ := ev["before"].(map[string]any)
	after, _ := ev["after"].(map[string]any)
	if ev == nil || ev["by"] != "human" || ev["decision"] != d.ID || before["longest_chain"] != float64(4) || before["width"] != float64(1) ||
		after["longest_chain"] != float64(2) || after["width"] != float64(3) || len(ev["changed"].([]any)) != 3 {
		t.Errorf("plan_replanned %v", ev)
	}
	if ids, _ := ev["phases_changed"].([]any); len(ids) != 3 || ids[0] != "P3" {
		t.Errorf("phases changed %v", ev["phases_changed"])
	}
	if _, err := os.Stat(r.s.DraftPath()); err == nil {
		t.Error("the draft is still there")
	}
	if r.read(r.s.ReplanText(1)) != chainDoc {
		t.Error("the text the replan found, its undo, is gone")
	}
	if _, err := Finish(r.s, "again", r.now); err == nil || err.Error() != decide.NoReplanOpen {
		t.Errorf("a second finish: %v", err)
	}
}

// A draft that changes what the replan may not is refused, with exactly what failed: the plan file is
// untouched, and the replan stays open for the model to fix the draft.
func TestARefusedDraftLeavesThePlanFileAndTheReplanOpen(t *testing.T) {
	r := newRig(t)
	r.open("")
	r.draft(strings.Replace(widened, "Build the API.", "Build the API, and the CLI too.", 1))
	_, err := Finish(r.s, "merged", r.now)
	var refused Refused
	if !errors.As(err, &refused) || !strings.Contains(refused.Text, "not replanned — the draft changes what this replan may not") ||
		!strings.Contains(refused.Text, "- P1 has started or is done: its text must stay byte for byte as it was") {
		t.Fatalf("refusal: %v", err)
	}
	if r.read(r.file) != chainDoc || r.state().Replan == nil || r.read(r.s.DraftPath()) == chainDoc {
		t.Fatal("the plan file changed, the replan closed, or the draft was dropped")
	}
	if ev := r.event("replan_refused"); ev == nil || len(ev["breaches"].([]any)) != 1 {
		t.Errorf("replan_refused %v", ev)
	}
	r.draft(widened)
	if _, err := Finish(r.s, "fixed", r.now); err != nil {
		t.Fatalf("the fixed draft: %v", err)
	}
}

// The human's words let the replan drop a phase not started, as far as they ask; the notes name it.
// Without words, every phase not started stays.
func TestAHumanRunDrop(t *testing.T) {
	dropped := strings.Replace(chainDoc, "## P3 — Docs\n\nWrite the docs.\n\n", "", 1)
	t.Run("without words", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		r.draft(dropped)
		_, err := Finish(r.s, "P3 is not needed", r.now)
		if !strings.Contains(err.Error(), "P3 is gone: this replan keeps every phase not started") || r.read(r.file) != chainDoc {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("with words, the notes silent on it", func(t *testing.T) {
		r := newRig(t)
		if o := r.open("the docs are done elsewhere"); !strings.Contains(o.Brief, "merge or drop phases not started, only as far as the human's words ask") ||
			!strings.Contains(o.Brief, `The human asked you to consider: "the docs are done elsewhere".`) {
			t.Fatalf("brief:\n%s", o.Brief)
		}
		r.draft(dropped)
		_, err := Finish(r.s, "the docs are done elsewhere", r.now)
		if err == nil || !strings.Contains(err.Error(), "P3 is dropped, and your --notes do not name it") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("with words, the notes naming it", func(t *testing.T) {
		r := newRig(t)
		r.open("the docs are done elsewhere")
		r.draft(dropped)
		f, err := Finish(r.s, "dropped P3: the docs are done elsewhere", r.now)
		if err != nil {
			t.Fatal(err)
		}
		st := r.state()
		if st.Phases["P3"] != nil || r.plan().Index("P3") >= 0 || r.plan().Deps("P4")[0] != "P2" {
			t.Errorf("P3 still there: %+v", st.Phases)
		}
		if got := decide.ReplanChanges(f.Replanned.Changed); got != "P4 now after P2 (was after P3); P3 dropped" {
			t.Errorf("changes %q", got)
		}
	})
}

// --none closes the replan with the plan as it was, as does a draft that changed nothing; a plan file
// changed while the replan was open closes it too, its draft refused.
func TestAReplanClosedWithThePlanUnchanged(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		if _, err := Decline(r.s, "every link of the chain holds", r.now); err != nil {
			t.Fatal(err)
		}
		st := r.state()
		if st.Replan != nil || !strings.Contains(st.Run.Announce[len(st.Run.Announce)-1], "baton: replan closed, the plan unchanged — every link of the chain holds") {
			t.Fatalf("replan %+v, announced %q", st.Replan, st.Run.Announce)
		}
		if ev := r.event("replan_declined"); ev == nil || ev["why"] != "every link of the chain holds" || ev["by"] != "human" {
			t.Errorf("replan_declined %v", ev)
		}
		for _, path := range []string{r.s.DraftPath(), r.s.ReplanText(1)} {
			if _, err := os.Stat(path); err == nil {
				t.Errorf("%s is still there", path)
			}
		}
		if _, err := Decline(r.s, "again", r.now); err == nil {
			t.Error("a second decline")
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		f, err := Finish(r.s, "nothing to change", r.now)
		if err != nil || !f.Unchanged || r.state().Replan != nil || r.event("replan_declined")["unchanged"] != true {
			t.Fatalf("finish %+v, %v", f, err)
		}
	})
	t.Run("the plan file changed meanwhile", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		os.WriteFile(r.file, []byte(chainDoc+"\nA note.\n"), 0o644)
		r.draft(widened)
		if _, err := Finish(r.s, "widened", r.now); err == nil || err.Error() != decide.ReplanStale {
			t.Fatalf("err %v", err)
		}
		if r.state().Replan != nil || r.read(r.file) != chainDoc+"\nA note.\n" || r.event("replan_refused")["closed"] != true {
			t.Error("the replan stayed open, or the plan file changed")
		}
	})
}

// A replan open longer than Max lapses: its draft is dropped and the plan goes on as it was, once.
func TestAReplanLapses(t *testing.T) {
	r := newRig(t)
	r.open("")
	if !Lapse(r.s, 1, r.now.Add(Max), Max) || r.state().Replan != nil {
		t.Fatal("not lapsed")
	}
	if ev := r.event("replan_lapsed"); ev == nil || ev["after"] != "20m0s" || ev["n"] != float64(1) {
		t.Errorf("replan_lapsed %v", ev)
	}
	if a := r.state().Run.Announce; !strings.Contains(a[len(a)-1], "baton: the replan was not finished within 20 minutes → its draft is dropped") {
		t.Errorf("announced %q", a)
	}
	if _, err := os.Stat(r.s.DraftPath()); err == nil || r.read(r.file) != chainDoc {
		t.Error("the draft is still there, or the plan file changed")
	}
	if Lapse(r.s, 1, r.now.Add(Max), Max) {
		t.Error("lapsed twice")
	}
}

// A replan open already is given again; the human's words take it over. A plan whose every phase has
// started has nothing to replan, and no plan running has none either.
func TestOpeningAReplan(t *testing.T) {
	r := newRig(t)
	r.open("")
	o := r.open("the CI phase can wait")
	if !o.Again || o.Replan.N != 1 || o.Replan.Words != "the CI phase can wait" || r.state().Replan.Words != "the CI phase can wait" {
		t.Errorf("again %+v", o)
	}
	if !strings.Contains(o.Brief, "merge or drop phases not started") {
		t.Errorf("the brief again:\n%s", o.Brief)
	}

	r = newRig(t)
	r.s.Update(func(st *state.State) error {
		for _, id := range []string{"P1", "P2", "P3", "P4"} {
			st.Phases[id] = &state.PhaseState{Status: state.PhaseDone}
		}
		st.Phases["P5"], st.Current = &state.PhaseState{Status: state.PhaseActive}, "P5"
		return nil
	})
	if _, err := Open(r.s, state.ReplanHuman, "", r.now, Max); err == nil || err.Error() != decide.NothingToReplan {
		t.Errorf("every phase started: %v", err)
	}
	r.s.Update(func(st *state.State) error { return state.Detach(st) })
	if _, err := Open(r.s, state.ReplanHuman, "", r.now, Max); err == nil || !strings.Contains(err.Error(), "no plan is running here") {
		t.Errorf("no plan: %v", err)
	}
}

// A phase that started between the check and the run following the draft is checked again; a draft that
// changed it is refused, and the plan file goes back as it was. A panic midway does the same.
func TestWhatHappensMidway(t *testing.T) {
	t.Run("a phase started meanwhile", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		r.draft(strings.Replace(widened, "Write the docs.", "Write the docs, with examples.", 1))
		defer func() { midway = func() {} }()
		midway = func() {
			r.s.Update(func(st *state.State) error {
				st.Phases["P3"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "sess-1/P3"}
				return nil
			})
		}
		_, err := Finish(r.s, "widened", r.now)
		var refused Refused
		if !errors.As(err, &refused) || refused.Breaches[0].Phase != "P3" || r.read(r.file) != chainDoc || r.state().Replan == nil {
			t.Fatalf("err %v, plan file changed %v", err, r.read(r.file) != chainDoc)
		}
	})
	t.Run("a panic", func(t *testing.T) {
		r := newRig(t)
		r.open("")
		r.draft(widened)
		defer func() { midway = func() {} }()
		midway = func() { panic("boom") }
		if _, err := Finish(r.s, "widened", r.now); err == nil || !strings.Contains(err.Error(), "the replan failed midway (boom)") {
			t.Fatalf("err %v", err)
		}
		if r.read(r.file) != chainDoc || r.state().Replan == nil || r.plan().Deps("P3")[0] != "P2" {
			t.Error("the plan file, the plan or the replan changed")
		}
	})
}

// A replan's kept text copied back over the plan file undoes it: baton follows the text, dropping the
// phases the replan added, as long as none of them started; the replans it undoes say so.
func TestARestoreUndoesAReplan(t *testing.T) {
	split := strings.Replace(chainDoc, "## P3 — Docs", "## P2b — CLI docs\n\nAfter: P2\n\nDocument the CLI.\n\n## P3 — Docs", 1)
	for _, c := range []struct {
		name    string
		started bool
		err     string
	}{
		{"nothing it added started", false, ""},
		{"a phase it added started since", true, "P2b started since, and the text copied back lacks it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.open("")
			r.draft(split)
			if _, err := Finish(r.s, "split the CLI docs out", r.now); err != nil {
				t.Fatal(err)
			}
			if c.started {
				r.s.Update(func(st *state.State) error {
					st.Phases["P2b"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "sess-1/P2b"}
					return nil
				})
			}
			os.WriteFile(r.file, []byte(chainDoc), 0o644) // the undo
			var u Restored
			var ok bool
			r.s.Update(func(st *state.State) error {
				u, ok = Restore(st, r.s, r.plan(), r.now)
				return nil
			})
			if !ok || u.N != 1 {
				t.Fatalf("not a restore: %+v", u)
			}
			if c.err != "" {
				if u.Err == nil || u.Err.Error() != c.err || r.plan().Index("P2b") < 0 {
					t.Fatalf("err %v", u.Err)
				}
				return
			}
			st := r.state()
			if u.Err != nil || st.Phases["P2b"] != nil || r.plan().Index("P2b") >= 0 || r.plan().SHA() != r.plan().SHA256 || st.Replans[0].Undone.IsZero() {
				t.Fatalf("err %v, phases %v, replans %+v", u.Err, st.Phases, st.Replans)
			}
			if got := decide.ReplanChanges(u.Changes); got != "P3 now after P2 (was after P2b); P2b dropped" {
				t.Errorf("changes %q", got)
			}
		})
	}
	r := newRig(t)
	os.WriteFile(r.file, []byte(widened), 0o644) // an edit of the human's, no kept text
	r.s.Update(func(st *state.State) error {
		if _, ok := Restore(st, r.s, r.plan(), r.now); ok {
			t.Error("an edit read as a restore")
		}
		return nil
	})
}
