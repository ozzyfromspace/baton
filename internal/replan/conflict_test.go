package replan

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Naming finds the phases whose sections name each path, in the plan's order, among the phases asked
// about: a path named as written, or by its end from a slash on; never by the standing rules, and never by
// a name that only ends the same way without a slash.
func TestNaming(t *testing.T) {
	doc := []byte(sharedDoc)
	pl, err := plan.Build("files.md", doc, plan.Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"P0", "P1", "P2", "P3", "P4"}
	paths := []string{"cmd/baton/main.go", "go.mod", "internal/state/state.go", "src/web/app.ts", "xweb/app.ts", "nothing.go"}
	for _, c := range []struct {
		name string
		ids  []string
		want map[string][]string
	}{
		{"every phase", all, map[string][]string{"cmd/baton/main.go": {"P2", "P3"}, "go.mod": {"P1", "P4"}, "internal/state/state.go": {"P0", "P2"}, "src/web/app.ts": {"P4"}}},
		{"the phases not started", []string{"P4", "P2"}, map[string][]string{"cmd/baton/main.go": {"P2"}, "go.mod": {"P4"}, "internal/state/state.go": {"P2"}, "src/web/app.ts": {"P4"}}},
		{"none", nil, map[string][]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Naming(pl, doc, c.ids, paths); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Naming = %v, want %v", got, c.want)
			}
		})
	}
	if got := Naming(pl, nil, all, paths); len(got) != 0 {
		t.Errorf("Naming with no text = %v", got)
	}
}

// A file in a second landing conflict that phases not started name cues a replan of baton's own at the
// lead's next boundary, once a file: its brief names the files and the phases, and it may reword and split
// phases, never drop or merge them. One whose phases have all started is settled with nothing opened, and
// a marked phase's replan, wider, settles it too, its brief saying so.
func TestTheConflictReplanOpensOnce(t *testing.T) {
	if got := LimitsOf(state.Replan{By: state.ReplanConflict}); got != (plan.Limits{Add: true, Edit: true}) {
		t.Errorf("limits %+v", got)
	}

	r := rigOf(t, sharedDoc, "P0") // P1 in hand; P2, P3 and P4 not started
	cue := func(r *rig, m map[string][]string) {
		r.s.Update(func(st *state.State) error {
			for p, ids := range m {
				state.CueConflict(st, []string{p}, map[string][]string{p: ids})
			}
			return nil
		})
	}
	cue(r, map[string][]string{"cmd/baton/main.go": {"P2", "P3"}})
	if c := Due(r.state(), r.plan()); c.By != state.ReplanConflict || !slices.Equal(c.Phases, []string{"P2", "P3"}) || !slices.Equal(c.Files, []string{"cmd/baton/main.go"}) {
		t.Fatalf("due %+v", c)
	}
	o, ok := r.auto()
	if !ok || o.Replan.By != state.ReplanConflict || !slices.Equal(o.Replan.Files, []string{"cmd/baton/main.go"}) || o.Replan.Then != "P1" {
		t.Fatalf("opened %v: %+v", ok, o.Replan)
	}
	checkBrief(t, o, []string{
		"replan the phases of \"Files\" that have not started, so the phases that name the files that conflicted stop colliding when they land. Do it first",
		"Why now: cmd/baton/main.go (named by P2 and P3) conflicted in a second landing of this run, and phases not started name it. " +
			"One file that phases running at once each change serializes them",
		"Give each phase not started that names it a file of its own, and have a QA phase merge them",
		"- split a phase not started",
		"- reword a phase not started so it works on a file of its own",
		"- no phase not started is dropped or merged away",
	}, []string{"baton keeps the draft only if", "The human"})
	if a := r.state().Run.Announce; !strings.Contains(a[len(a)-1], "(a file that conflicted in a second landing is named by P2 and P3, not started)") {
		t.Errorf("announced %q", a)
	}
	if ev := r.event("replan_opened"); ev == nil || ev["by"] != "conflict" || !reflect.DeepEqual(ev["files"], []any{"cmd/baton/main.go"}) {
		t.Errorf("replan_opened %v", ev)
	}
	if !r.state().Cued(state.ReplanConflict, "cmd/baton/main.go") {
		t.Error("the file is not cued")
	}
	if _, err := Decline(r.s, "they land one after the other", r.now); err != nil {
		t.Fatal(err)
	}
	cue(r, map[string][]string{"cmd/baton/main.go": {"P2", "P3"}}) // a third conflict cues nothing again
	if _, ok := r.auto(); ok || r.count("replan_opened") != 1 {
		t.Error("replanned twice for one file")
	}

	// Every phase that named it has started: settled once, nothing opened.
	r = rigOf(t, sharedDoc, "P0")
	cue(r, map[string][]string{"go.mod": {"P1"}})
	if _, ok := r.auto(); ok || r.event("replan_skipped")["why"] != "the phases that named the files that conflicted have started" || !r.state().Cued(state.ReplanConflict, "go.mod") {
		t.Errorf("opened %v; skipped %v", ok, r.event("replan_skipped"))
	}
	if _, ok := r.auto(); ok || r.count("replan_skipped") != 1 {
		t.Error("looked twice")
	}

	// A marked phase done opens its own replan, which settles the conflict too and names it.
	r = rigOf(t, markedDoc, "P0")
	cue(r, map[string][]string{"docs/guide.md": {"P2"}})
	o, ok = r.auto()
	if !ok || o.Replan.By != state.ReplanMarked || !slices.Equal(o.Replan.Files, []string{"docs/guide.md"}) || !r.state().Cued(state.ReplanConflict, "docs/guide.md") {
		t.Fatalf("opened %v: %+v", ok, o.Replan)
	}
	checkBrief(t, o, []string{"Why now: P0 (Spike) is done", " Also: docs/guide.md conflicted in a second landing of this run"}, nil)
}
