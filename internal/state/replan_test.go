package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// phases is a plan of ids, each waiting for the ones after (a list of "id:dep,dep").
func phases(specs ...string) plan.Plan {
	var p plan.Plan
	for _, s := range specs {
		id, deps, _ := strings.Cut(s, ":")
		after := []string{}
		if deps != "" {
			after = strings.Split(deps, ",")
		}
		p.Phases = append(p.Phases, plan.Phase{ID: id, Title: id, After: after})
	}
	return p
}

// A replan may not change a phase that started or is done, one given to a worker, or the phase in hand
// even before its brief.
func TestFrozen(t *testing.T) {
	p := phases("P0", "P1:P0", "P2:P1", "P3:P2", "P4:P3", "P5:P4", "P6:P5")
	st := Attach(p, t0, Origin{})
	st.Phases["P0"].Status = PhaseDone
	st.Phases["P1"] = &PhaseState{Status: PhaseWorking, Worker: "sess/P1"}
	st.Phases["P2"] = &PhaseState{Status: PhaseParked}
	st.Phases["P3"] = &PhaseState{Status: PhasePending, Worker: "sess/P3"}
	st.Phases["P5"] = &PhaseState{Status: PhaseVerifying}
	st.Current = "P4"
	st.Phases["P4"] = &PhaseState{Status: PhasePending}
	st.BoundaryOwed = true
	want := map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true, "P4": true, "P5": true}
	if got := Frozen(st, p); !reflect.DeepEqual(got, want) {
		t.Errorf("Frozen = %v, want %v", got, want)
	}
	if got := Unstarted(st, p); !reflect.DeepEqual(got, []string{"P6"}) {
		t.Errorf("Unstarted = %v", got)
	}
}

// A replan the run follows keeps every phase it keeps as it stands, adds its new phases pending, drops
// the phases not started it drops (cancelling a job of one), and takes up a phase it lets start when the
// run holds none; one that drops a phase started since is refused.
func TestAdoptReplan(t *testing.T) {
	old := phases("P0", "P1:P0", "P2:P1", "P3:P2")
	for _, c := range []struct {
		name    string
		next    plan.Plan
		set     func(st *State)
		took    string
		current string
		mode    string
		err     string
		phases  string
	}{
		{"a phase dropped, one added", phases("P0", "P1:P0", "P2:P1", "P2b:P1"), nil, "", "P1", ModeRunning, "", "P0 P1 P2 P2b"},
		{"a phase that started since, dropped", phases("P0", "P1:P0", "P2:P1"), func(st *State) {
			st.Phases["P3"] = &PhaseState{Status: PhaseWorking, Worker: "sess/P3"}
		}, "", "P1", ModeRunning, "P3 has started since", "P0 P1 P2 P3"},
		{"no phase in hand, and one free to start now", phases("P0", "P1:P0", "P2:P0", "P3:P2"), func(st *State) {
			st.Phases["P1"] = &PhaseState{Status: PhaseWorking, Worker: "sess/P1"}
			st.Current = ""
		}, "P2", "P2", ModeRunning, "", "P0 P1 P2 P3"},
		{"nothing left to do", phases("P0", "P1:P0"), func(st *State) {
			st.Phases["P1"].Status = PhaseDone
			st.Current = ""
		}, "", "", ModeComplete, "", "P0 P1"},
		{"paused: nothing is taken up", phases("P0", "P1:P0", "P2:P0", "P3:P2"), func(st *State) {
			st.Phases["P1"] = &PhaseState{Status: PhaseWorking, Worker: "sess/P1"}
			st.Current, st.Mode = "", ModePaused
		}, "", "", ModePaused, "", "P0 P1 P2 P3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := Attach(old, t0, Origin{})
			st.Phases["P0"].Status = PhaseDone
			st.Current = "P1"
			st.Phases["P1"] = &PhaseState{Status: PhaseActive}
			st.Jobs = []Job{{ID: "j1", Phase: "P3", Status: JobQueued}}
			st.PlanDrift = "abc"
			if c.set != nil {
				c.set(&st)
			}
			took, err := AdoptReplan(&st, c.next, t0)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v, want %q", err, c.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, ph := range append(old.Phases, plan.Phase{ID: "P2b"}) {
				if st.Phases[ph.ID] != nil {
					ids = append(ids, ph.ID)
				}
			}
			if took != c.took || st.Current != c.current || st.Mode != c.mode || strings.Join(ids, " ") != c.phases {
				t.Errorf("took %q, current %q, mode %s, phases %v", took, st.Current, st.Mode, ids)
			}
			if c.err == "" && c.next.Index("P3") < 0 && st.Jobs[0].Status != JobInterrupted {
				t.Errorf("the dropped phase's job: %+v", st.Jobs[0])
			}
			if c.err == "" && st.PlanDrift != "" {
				t.Errorf("plan drift %q", st.PlanDrift)
			}
		})
	}
}

// A replan baton followed is recorded with a decision of the run's own (no phase's review counts it),
// shown in the session already, with its undo.
func TestRecordReplan(t *testing.T) {
	st := Attach(phases("P0", "P1:P0"), t0, Origin{})
	st.IDPrefix = ""
	d := RecordReplan(&st, Replanned{N: 1, By: ReplanHuman, At: t0}, "baton replanned the phases not started", "copy 1.md back")
	if d.ID != "d1" || d.Kind != Note || d.Phase != "" || !d.Announced || d.Undo != "copy 1.md back" || !d.WithoutHuman() {
		t.Errorf("decision %+v", d)
	}
	if len(st.Replans) != 1 || st.Replans[0].Decision != "d1" || st.Unattended("P0") != 0 {
		t.Errorf("replans %+v", st.Replans)
	}
}

// A replan opens on a run running or paused, one at a time.
func TestOpenReplan(t *testing.T) {
	st := Attach(phases("P0", "P1:P0"), t0, Origin{})
	if err := OpenReplan(&st, Replan{By: ReplanHuman, Since: t0, N: 1}); err != nil || st.Replan == nil {
		t.Fatalf("open: %v", err)
	}
	if err := OpenReplan(&st, Replan{By: ReplanHuman, Since: t0, N: 2}); err == nil || st.Replan.N != 1 {
		t.Errorf("a second replan: %v", err)
	}
	idle := New()
	if err := OpenReplan(&idle, Replan{By: ReplanHuman}); err == nil {
		t.Error("a replan of no plan opened")
	}
}

// The replan folder keeps the approved text (0.md), and each replan's (n.md): numbered after the highest
// kept, found again when one is copied back, and started afresh for a new plan, the old folder kept with
// the plan it belonged to.
func TestReplanFiles(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run"), "inst", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if n := s.NextReplan(); n != 1 {
		t.Errorf("first replan %d", n)
	}
	if err := s.KeepApproved([]byte("approved"), ""); err != nil {
		t.Fatal(err)
	}
	s.WriteReplanText(1, []byte("first"), true)
	s.WriteReplanText(0, []byte("not again"), false) // kept already: left as it is
	s.WriteDraft([]byte("draft"))
	if n := s.NextReplan(); n != 2 {
		t.Errorf("next replan %d", n)
	}
	if n, ok := s.Restores([]byte("approved")); !ok || n != 0 {
		t.Errorf("restores the approved text: %d %v", n, ok)
	}
	if _, ok := s.Restores([]byte("draft")); ok {
		t.Error("a draft is no kept text")
	}
	s.WriteReplanText(2, []byte("first"), true)
	if n, ok := s.Restores([]byte("first")); !ok || n != 2 {
		t.Errorf("the latest of two same texts: %d %v", n, ok)
	}
	s.DropReplan(2)
	if _, err := os.Stat(s.ReplanText(2)); err == nil {
		t.Error("a dropped replan's text is still there")
	}
	if _, err := os.Stat(s.DraftPath()); err == nil {
		t.Error("a dropped replan's draft is still there")
	}
	kept := filepath.Join(s.Dir, "gen-1")
	os.MkdirAll(kept, 0o755)
	if err := s.KeepApproved([]byte("the next plan"), kept); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(kept, "replan", "0.md")); string(b) != "approved" {
		t.Errorf("the old plan's approved text, kept: %q", b)
	}
	if b, _ := os.ReadFile(s.ReplanText(0)); string(b) != "the next plan" || s.NextReplan() != 1 {
		t.Errorf("the new plan's approved text %q, next replan %d", b, s.NextReplan())
	}
}

// The plan file is replaced whole, keeping its mode.
func TestReplacePlanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(path, []byte("old"), 0o600)
	was, _ := os.Stat(path) // 0600, where the system keeps such a mode (not on Windows)
	if err := ReplacePlanFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(b) != "new" || fi.Mode().Perm() != was.Mode().Perm() {
		t.Errorf("%q, mode %v", b, fi.Mode())
	}
}
