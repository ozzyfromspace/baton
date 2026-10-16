package replan

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// qaDoc is a plan whose QA phase, P1, comes early, before six phases in one chain.
const qaDoc = `# Checks

## Standing rules

- Be careful.

## P0 — Build

Build it.

## P1 — Check

After: P0
Type: qa

Check it.

## P2 — A

After: P1

Do A.

## P3 — B

Do B.

## P4 — C

Do C.

## P5 — D

Do D.

## P6 — E

Do E.

## P7 — F

Do F.

## Verification

All green.
`

// wideQADoc is qaDoc with the six phases after its QA phase independent of each other.
var wideQADoc = strings.NewReplacer("Do B.", "After: P1\n\nDo B.", "Do C.", "After: P1\n\nDo C.", "Do D.", "After: P1\n\nDo D.",
	"Do E.", "After: P1\n\nDo E.", "Do F.", "After: P1\n\nDo F.").Replace(qaDoc)

// markedDoc is a chain whose first phase, a spike, is marked Replan: after.
const markedDoc = `# Spiked

## Standing rules

- Be careful.

## P0 — Spike

Replan: after

Try it.

## P1 — Build

Build it.

## P2 — Docs

Write the docs.

## P3 — Ship

Ship it.

## Verification

All green.
`

// rigOf is a run of doc, attached as the human approved it, with the phases done finished in turn and the
// next one in hand.
func rigOf(t *testing.T, doc string, done ...string) *rig {
	t.Helper()
	r := &rig{t: t, now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	r.file = filepath.Join(dir, "plan.md")
	os.WriteFile(r.file, []byte(doc), 0o644)
	var err error
	if r.s, err = state.Open(filepath.Join(dir, ".baton", "runs", "sess-1"), "inst", func() time.Time { return r.now }); err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build(r.file, []byte(doc), plan.Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	r.s.SavePlan(pl)
	r.s.Update(func(st *state.State) error {
		*st = state.Attach(pl, r.now, state.Origin{})
		for _, id := range done {
			if _, err := state.Done(st, pl, id, r.now, state.DoneOpts{}); err != nil {
				t.Fatal(err)
			}
			state.Start(st, r.now, state.Origin{})
		}
		return nil
	})
	KeepApproved(r.s, pl, "")
	return r
}

// auto is Auto at the boundary where the run begins the phase in hand.
func (r *rig) auto() (Opened, bool) {
	r.t.Helper()
	o, ok, err := Auto(r.s, r.state().Current, r.now, Max)
	if err != nil {
		r.t.Fatal(err)
	}
	return o, ok
}

// count is how many events of kind the run logged.
func (r *rig) count(kind string) int {
	return strings.Count(r.read(filepath.Join(r.s.Dir, "events.jsonl")), `"kind":"`+kind+`"`)
}

// The brief of a replan baton opens by itself says why, what it may change, and that the phase in hand
// begins once it is finished; the human is told.
func checkBrief(t *testing.T, o Opened, wants, lacks []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(o.Brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, o.Brief)
		}
	}
	for _, lack := range lacks {
		if strings.Contains(o.Brief, lack) {
			t.Errorf("the brief says %q:\n%s", lack, o.Brief)
		}
	}
}

// A run whose plan was made under older planning rules (a run carried across an upgrade) is replanned once
// by the newer ones, at its first phase boundary, for its structure alone; never again for those rules.
func TestTheUpgradeReplanOpensOnce(t *testing.T) {
	r := newRig(t)
	if st := r.state(); st.RulesVersion != plan.RulesVersion {
		t.Fatalf("a plan attached now records rules %d, want %d", st.RulesVersion, plan.RulesVersion)
	}
	if _, ok := r.auto(); ok {
		t.Fatal("a plan made under the rules of now was replanned")
	}
	r.s.Update(func(st *state.State) error { st.RulesVersion = 0; return nil }) // a run from before
	o, ok := r.auto()
	if !ok || o.Replan.By != state.ReplanUpgrade || o.Replan.Then != "P1" || len(o.Replan.Cue) != 0 {
		t.Fatalf("opened %v: %+v", ok, o.Replan)
	}
	checkBrief(t, o, []string{
		`[baton] Before you begin P1: replan the phases of "Chains" that have not started`,
		"Why now: this plan was made under older planning rules",
		"1. A dependency is a need.",
		"- nothing else of a phase: its heading, its steps and its done-when stay byte for byte",
		"- no phase not started is dropped or merged away",
		"baton keeps the draft only if it shortens the longest chain of the phases not started (4 now) or lets more of them run at once (1 now)",
		"Once baton replanned has answered, begin P1, as its brief below says.",
	}, []string{"split a phase", "reword", "The human"})
	st := r.state()
	if st.RulesVersion != plan.RulesVersion || st.Replan == nil || st.Replan.By != state.ReplanUpgrade {
		t.Fatalf("rules %d, replan %+v", st.RulesVersion, st.Replan)
	}
	if a := st.Run.Announce; !strings.Contains(a[len(a)-1], "baton: replanning the phases not started (a newer baton's planning rules) → Claude does it before the next phase") {
		t.Errorf("announced %q", a)
	}
	if ev := r.event("replan_opened"); ev == nil || ev["by"] != "upgrade" || ev["then"] != "P1" {
		t.Errorf("replan_opened %v", ev)
	}
	if _, err := Decline(r.s, "the chain is real", r.now); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.auto(); ok || r.count("replan_opened") != 1 {
		t.Error("replanned twice for the same rules")
	}

	// A run whose phases not started have no chain to shorten looks once, and opens none.
	r = rigOf(t, wideQADoc, "P0", "P1")
	r.s.Update(func(st *state.State) error { st.RulesVersion = 0; return nil })
	if _, ok := r.auto(); ok || r.state().RulesVersion != plan.RulesVersion || r.event("replan_skipped")["why"] != "the phases not started have no chain to shorten" {
		t.Errorf("opened %v; rules %d; skipped %v", ok, r.state().RulesVersion, r.event("replan_skipped"))
	}
	if _, ok := r.auto(); ok || r.count("replan_skipped") != 1 {
		t.Error("looked twice")
	}
}

// At the start of a QA phase, baton replans the phases not started only when they are mostly one chain,
// and looks at each QA phase once.
func TestTheQAReplanOpensOnlyOverAChain(t *testing.T) {
	r := rigOf(t, qaDoc, "P0")
	o, ok := r.auto()
	if !ok || o.Replan.By != state.ReplanQA || !slices.Equal(o.Replan.Cue, []string{"P1"}) || o.Replan.Then != "P1" {
		t.Fatalf("opened %v: %+v", ok, o.Replan)
	}
	checkBrief(t, o, []string{
		"Why now: P1 (Check), a QA phase, starts, and the phases not started are mostly one chain.",
		"The phases not started now: 6 phases · longest chain 6 (P2 → P3 → P4 → P5 → P6 → P7) · one at a time.",
		"(6 now) or lets more of them run at once (1 now)",
	}, []string{"split a phase"})
	if a := r.state().Run.Announce; !strings.Contains(a[len(a)-1], "(P1, a QA phase, starts over a chain of phases not started)") {
		t.Errorf("announced %q", a)
	}
	if !r.state().Cued(state.ReplanQA, "P1") {
		t.Error("the QA phase is not cued")
	}
	if _, err := Decline(r.s, "each link is a need", r.now); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.auto(); ok || r.count("replan_opened") != 1 {
		t.Error("replanned twice at one QA phase")
	}

	r = rigOf(t, wideQADoc, "P0")
	if _, ok := r.auto(); ok || r.state().Replan != nil {
		t.Fatal("a QA phase over phases that can all run at once was replanned")
	}
	if ev := r.event("replan_skipped"); ev == nil || ev["why"] != "the phases not started are not mostly one chain" || !r.state().Cued(state.ReplanQA, "P1") {
		t.Errorf("replan_skipped %v", ev)
	}
}

// A phase marked Replan: after that is done has the phases not started replanned with what it found,
// once: the draft may rework their steps and split them, never drop or merge work, and it stands whatever
// its shape.
func TestTheMarkedPhasesReplanOpensOnce(t *testing.T) {
	r := rigOf(t, markedDoc, "P0")
	if pl := r.plan(); !pl.Phases[0].Marked() || pl.Phases[1].Marked() {
		t.Fatalf("phases %+v", pl.Phases)
	}
	o, ok := r.auto()
	if !ok || o.Replan.By != state.ReplanMarked || !slices.Equal(o.Replan.Cue, []string{"P0"}) {
		t.Fatalf("opened %v: %+v", ok, o.Replan)
	}
	checkBrief(t, o, []string{
		"Why now: P0 (Spike) is done, and the plan marks it `Replan: after`: what it found should reshape the phases not started.",
		"What it found is in its notes (" + r.s.HandoffPath() + ").",
		"- split a phase not started",
		"- rework the steps of a phase not started with what P0 found",
		"- no phase not started is dropped or merged away",
	}, []string{"baton keeps the draft only if"})
	if a := r.state().Run.Announce; !strings.Contains(a[len(a)-1], "(P0 is done, marked Replan: after)") {
		t.Errorf("announced %q", a)
	}

	dropped := strings.Replace(markedDoc, "## P3 — Ship\n\nShip it.\n\n", "", 1)
	r.draft(dropped)
	if _, err := Finish(r.s, "P3 is not needed", r.now); err == nil || !strings.Contains(err.Error(), "P3 is gone") || r.read(r.file) != markedDoc {
		t.Fatalf("a dropped phase: %v", err)
	}
	reworked := strings.Replace(markedDoc, "Write the docs.\n\n## P3 — Ship\n\nShip it.",
		"Write the docs, with the spike's numbers.\n\n## P3 — Ship\n\nShip it.\n\n## P3b — Announce\n\nAfter: P1\n\nAnnounce it.", 1)
	r.draft(reworked)
	f, err := Finish(r.s, "the spike found the numbers; announcing is its own work", r.now)
	if err != nil {
		t.Fatal(err)
	}
	if r.read(r.file) != reworked || r.plan().Index("P3b") < 0 || f.Unimproved {
		t.Fatalf("not followed: %+v", f)
	}
	if got := decide.ReplanChanges(f.Replanned.Changed); got != "P2 reworded; P3b added (after P1)" {
		t.Errorf("changes %q", got)
	}
	if ev := r.event("plan_replanned"); ev == nil || ev["by"] != "marked" {
		t.Errorf("plan_replanned %v", ev)
	}
	if _, ok := r.auto(); ok || r.count("replan_opened") != 1 {
		t.Error("replanned twice for one marked phase")
	}
}

// A replan of the structure alone (an upgrade's, a QA phase's) that changed a step is refused: the plan
// file is untouched, and the replan stays open for the model to fix the draft.
func TestAStructureOnlyReplanThatChangedAStepIsRefused(t *testing.T) {
	r := newRig(t)
	r.s.Update(func(st *state.State) error { st.RulesVersion = 0; return nil })
	if _, ok := r.auto(); !ok {
		t.Fatal("no replan")
	}
	r.draft(strings.Replace(widened, "Write the docs.", "Write the docs, twice.", 1))
	_, err := Finish(r.s, "widened", r.now)
	var refused Refused
	if !errors.As(err, &refused) || len(refused.Breaches) != 1 || refused.Breaches[0].Kind != plan.BreachEdited ||
		!strings.Contains(refused.Text, "- P3's text changed: this replan may change only its After: line and its place") {
		t.Fatalf("refusal: %v", err)
	}
	if r.read(r.file) != chainDoc || r.state().Replan == nil {
		t.Error("the plan file changed, or the replan closed")
	}
	split := strings.Replace(widened, "## P4 — Web", "## P3b — More docs\n\nAfter: P1\n\nMore.\n\n## P4 — Web", 1)
	r.draft(split)
	if _, err := Finish(r.s, "split", r.now); err == nil || !strings.Contains(err.Error(), "P3b is new: this replan may not add phases") {
		t.Errorf("a split: %v", err)
	}
}

// A replan of the structure alone is kept only if the longest chain of the phases not started got shorter
// or more of them can run at once: otherwise the plan file is untouched, replan_unchanged is logged, and
// the model is told why.
func TestTheAcceptanceRule(t *testing.T) {
	t.Run("no better", func(t *testing.T) {
		r := newRig(t)
		r.s.Update(func(st *state.State) error { st.RulesVersion = 0; return nil })
		r.auto()
		same := strings.Replace(chainDoc, "Write the docs.", "After: P2\n\nWrite the docs.", 1) // the same chain, said aloud
		r.draft(same)
		f, err := Finish(r.s, "said P3's need aloud", r.now)
		if err != nil || !f.Unimproved || f.Unchanged {
			t.Fatalf("finish %+v, %v", f, err)
		}
		if !strings.HasPrefix(f.Tell, "baton: not replanned — the draft leaves the phases not started no better (4 phases · longest chain 4 → 4 · up to 1 → 1 at once)") {
			t.Errorf("told %q", f.Tell)
		}
		st := r.state()
		if r.read(r.file) != chainDoc || st.Replan != nil || len(st.Replans) != 0 || f.State.Current != "P1" {
			t.Fatalf("the plan file changed, or the replan stayed open: %+v", st.Replan)
		}
		if a := st.Run.Announce; !strings.Contains(a[len(a)-1], "baton: replan closed, the plan unchanged — Claude's draft left the phases not started no better") {
			t.Errorf("announced %q", a)
		}
		ev := r.event("replan_unchanged")
		if before, _ := ev["before"].(map[string]any); ev == nil || ev["by"] != "upgrade" || before["longest_chain"] != float64(4) {
			t.Errorf("replan_unchanged %v", ev)
		}
		if _, err := os.Stat(r.s.DraftPath()); err == nil {
			t.Error("the draft is still there")
		}
	})
	t.Run("better", func(t *testing.T) {
		r := rigOf(t, qaDoc, "P0")
		r.auto()
		r.draft(wideQADoc)
		f, err := Finish(r.s, "each of A to F needs only the check", r.now)
		if err != nil || f.Unimproved {
			t.Fatalf("finish %+v, %v", f, err)
		}
		if r.read(r.file) != wideQADoc || len(f.After.Chain) != 1 || f.After.Width != 6 {
			t.Errorf("not kept: after %+v", f.After)
		}
		if ev := r.event("plan_replanned"); ev == nil || ev["by"] != "qa" || r.state().RulesVersion != plan.RulesVersion {
			t.Errorf("plan_replanned %v", ev)
		}
	})
}

// One boundary settles every cue it has with one replan, the widest; none is looked at while a replan is
// open, and none while the run does not run.
func TestOneBoundaryOneReplan(t *testing.T) {
	r := rigOf(t, markedDoc, "P0")
	r.s.Update(func(st *state.State) error { st.RulesVersion = 0; return nil })
	if c := Due(r.state(), r.plan()); c.By != state.ReplanMarked || !c.Rules {
		t.Fatalf("due %+v", c)
	}
	if o, ok := r.auto(); !ok || o.Replan.By != state.ReplanMarked || r.state().RulesVersion != plan.RulesVersion {
		t.Fatalf("opened %v %+v", ok, o.Replan)
	}

	// The human's /baton replan takes an open one over: its words and limits, and no phase to begin after.
	if o := r.open("the docs can wait"); !o.Again || o.Replan.By != state.ReplanHuman || o.Replan.Then != "" || strings.Contains(o.Brief, "Before you begin") ||
		!strings.Contains(o.Brief, "merge or drop phases not started") {
		t.Errorf("taken over: %+v\n%s", o.Replan, o.Brief)
	}

	r = rigOf(t, markedDoc, "P0")
	r.open("")
	if c := Due(r.state(), r.plan()); c.Any() {
		t.Errorf("due while a replan is open: %+v", c)
	}
	r.s.Update(func(st *state.State) error { st.Replan, st.Mode = nil, state.ModePaused; return nil })
	if c := Due(r.state(), r.plan()); c.Any() {
		t.Errorf("due while paused: %+v", c)
	}
}

// While the lead's phase boundary owes a replan of baton's own, new phase agents wait for it (Owed): from
// when the boundary was owed, or its brief, for at most max.
func TestAReplanOwedAtTheBoundary(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	doc := []byte(markedDoc)
	pl, err := plan.Build("spiked.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	st := state.Attach(pl, now, state.Origin{})
	state.Done(&st, pl, "P0", now, state.DoneOpts{}) // the boundary before P1 is owed
	for _, c := range []struct {
		name string
		set  func(st *state.State)
		at   time.Duration
		want bool
	}{
		{"the boundary owed", func(*state.State) {}, time.Minute, true},
		{"too long owed", func(*state.State) {}, Max, false},
		{"its brief owed", func(st *state.State) {
			state.Start(st, now, state.Origin{})
			st.Run.Briefing = &state.Briefing{Kind: "boundary", Phase: "P1", At: now.Add(time.Minute)}
		}, 2 * time.Minute, true},
		{"a checkpoint's brief owed", func(st *state.State) {
			state.Start(st, now, state.Origin{})
			st.Run.Briefing = &state.Briefing{Kind: "checkpoint", Phase: "P1", At: now}
		}, time.Minute, false},
		{"mid-phase", func(st *state.State) { state.Start(st, now, state.Origin{}) }, time.Minute, false},
		{"cued already", func(st *state.State) { st.Cue(state.ReplanMarked, "P0") }, time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := st
			s.Phases = map[string]*state.PhaseState{}
			for id, ps := range st.Phases {
				cp := *ps
				s.Phases[id] = &cp
			}
			c.set(&s)
			if got := Owed(s, pl, now.Add(c.at), Max); got != c.want {
				t.Errorf("Owed = %v, want %v", got, c.want)
			}
		})
	}
}
