package hooks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// spikedDoc is a chain whose first phase, a spike, is marked Replan: after.
const spikedDoc = "# Spiked\n\n## P0 — Spike\nReplan: after\n\nTry it.\n\n## P1 — Build\n\nBuild it.\n\n## P2 — Docs\n\nWrite the docs.\n\n## P3 — Ship\n\nShip it.\n"

// planned is a fixture running doc, attached now, P0 in hand, its lead's transcript marked since a
// compaction an hour ago.
func planned(t *testing.T, doc string) (*fixture, plan.Plan) {
	t.Helper()
	f := newFixture(t, false)
	docPath := filepath.Join(filepath.Dir(f.dir), "plan.md")
	os.WriteFile(docPath, []byte(doc), 0o644)
	pl, err := plan.Build(docPath, []byte(doc), plan.Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	f.store.SavePlan(pl)
	f.set(func(st *state.State) { *st = state.Reattach(*st, pl, f.now, state.Origin{}) })
	f.now = f.now.Add(-time.Hour)
	f.boundary("auto")
	f.now = f.now.Add(time.Hour)
	f.fire("PostToolUse", bash("ls"))
	return f, pl
}

// finishP0 finishes P0 and has baton compact the lead at the boundary: it returns the rewake's exit code
// and message, which carry P1's brief.
func (f *fixture) finishP0(pl plan.Plan) (int, string) {
	f.t.Helper()
	f.set(func(st *state.State) { state.Done(st, pl, "P0", f.now, state.DoneOpts{}) })
	f.fire("Stop", map[string]any{})
	f.now = f.now.Add(time.Minute)
	return f.compacted("manual")
}

// At the lead's boundary after a phase marked Replan: after, baton opens a replan, and its brief comes
// before the next phase's in the same context: replan, then begin the phase. Once.
func TestTheBoundaryAfterAMarkedPhaseOpensItsReplan(t *testing.T) {
	f, pl := planned(t, spikedDoc)
	code, msg := f.finishP0(pl)
	if code != 2 {
		t.Fatalf("no rewake (%d): %s", code, msg)
	}
	replan := strings.Index(msg, `[baton] Before you begin P1: replan the phases of "Spiked" that have not started, with what P0 found.`)
	phase := strings.Index(msg, "Now: P1 — Build. Begin it now.")
	if replan < 0 || phase < 0 || replan > phase {
		t.Fatalf("the replan's brief does not come before the phase's:\n%s", msg)
	}
	if !strings.HasSuffix(msg, "[baton] Context compacted. Replan first, as the brief above says (finish with baton replanned), then begin P1 (Build) — its brief above has your instructions.") {
		t.Errorf("the rewake's last line:\n%s", msg)
	}
	st := f.state()
	if r := st.Replan; r == nil || r.By != state.ReplanMarked || r.Then != "P1" || !slices.Equal(r.Cue, []string{"P0"}) || st.Current != "P1" {
		t.Fatalf("replan %+v, current %s", st.Replan, st.Current)
	}
	if !f.logged("replan_opened", map[string]any{"by": "marked", "then": "P1"}) || !f.logged("brief", map[string]any{"type": "boundary"}) {
		t.Errorf("events:\n%s", f.eventLog())
	}

	// The model finds nothing to change; at the next boundary, nothing opens again.
	f.set(func(st *state.State) { st.Replan = nil })
	f.set(func(st *state.State) { state.Done(st, pl, "P1", f.now, state.DoneOpts{}) })
	f.fire("Stop", map[string]any{})
	f.now = f.now.Add(time.Minute)
	if code, msg := f.compacted("manual"); code != 2 || strings.Contains(msg, "Replan first") || f.state().Replan != nil {
		t.Errorf("a second replan for one marked phase (%d): %+v\n%s", code, f.state().Replan, msg)
	}
}

// A brief given with a tool call carries the replan too: here a run carried across an upgrade of the
// planning rules, at its first boundary.
func TestABoundaryBriefWithAToolCallCarriesTheUpgradeReplan(t *testing.T) {
	f, pl := planned(t, "# Chain\n\n## P0 — A\n\nDo a.\n\n## P1 — B\n\nDo b.\n\n## P2 — C\n\nDo c.\n\n## P3 — D\n\nDo d.\n")
	f.set(func(st *state.State) {
		state.Done(st, pl, "P0", f.now, state.DoneOpts{})
		state.Start(st, f.now, state.Origin{})
		st.RulesVersion = 0
		st.Run.Briefing = &state.Briefing{Kind: "boundary", Phase: "P1", At: f.now}
	})
	msg := feedback(f.fire("PostToolUse", bash("ls")))
	replan, phase := strings.Index(msg, "Why now: this plan was made under older planning rules"), strings.Index(msg, "Now: P1 — B. Begin it now.")
	if replan < 0 || phase < 0 || replan > phase {
		t.Fatalf("the replan's brief does not come before the phase's:\n%s", msg)
	}
	if st := f.state(); st.Replan == nil || st.Replan.By != state.ReplanUpgrade || st.RulesVersion != plan.RulesVersion {
		t.Errorf("replan %+v, rules %d", st.Replan, st.RulesVersion)
	}
}

// With auto_replan off, baton opens no replan of its own; the phase's brief comes as it would.
func TestAutoReplanOff(t *testing.T) {
	f, pl := planned(t, spikedDoc)
	f.vars = map[string]string{"BATON_AUTO_REPLAN": "0"}
	code, msg := f.finishP0(pl)
	if code != 2 || strings.Contains(msg, "Before you begin") || !strings.Contains(msg, "[baton] Context compacted. Begin P1 (Build) now") {
		t.Fatalf("rewake (%d):\n%s", code, msg)
	}
	if st := f.state(); st.Replan != nil || f.logged("replan_opened", nil) || len(st.ReplanCued) != 0 {
		t.Errorf("replan %+v, cued %v", st.Replan, st.ReplanCued)
	}
}

// The automatic replan fails open: a panic in it costs the replan, never the phase's brief.
func TestTheAutoReplanFailsOpen(t *testing.T) {
	f, pl := planned(t, spikedDoc)
	defer func() { autoReplanning = func() {} }()
	autoReplanning = func() { panic("boom") }
	code, msg := f.finishP0(pl)
	if code != 2 || !strings.Contains(msg, "Now: P1 — Build. Begin it now.") || strings.Contains(msg, "Replan first") || f.state().Replan != nil {
		t.Fatalf("rewake (%d):\n%s", code, msg)
	}
	failsOpen(t, []failCase{
		{"a boundary's brief with a tool call", "PostToolUse", func(string) map[string]any { return bash("ls") }, func(f *fixture) {
			f.set(func(st *state.State) {
				st.RulesVersion = 0
				st.Run.Briefing = &state.Briefing{Kind: "boundary", Phase: "P0", At: f.now}
			})
		}},
	})
}
