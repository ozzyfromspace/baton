package state

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// lookRun is a running plan whose phase P3 a gate checks, after `baton done` moved the run on to P4: the
// gate's phase is not the one in hand.
func lookRun(j Job) State {
	st := New()
	st.Mode, st.Current = ModeRunning, "P4"
	st.Phases["P3"] = &PhaseState{Status: PhaseVerifying, On: []string{j.ID}}
	st.Phases["P4"] = &PhaseState{Status: PhaseActive}
	st.Jobs = []Job{j}
	return st
}

// A gate's first failure is told while it runs, whatever phase is in hand, once; not while a compaction is
// owed or the run is not running, and not for a failure in a stage the gate ran with --keep-going, unless a
// later stage failed without it.
func TestAGatesFirstFailureIsToldOnce(t *testing.T) {
	first := &Failing{At: t0.Add(8 * time.Minute), Line: "--- FAIL: TestLogin (0.20s)", Stage: "e2e"}
	gate := Job{ID: "j3", Phase: "P3", Kind: JobVerify, Command: "make e2e", Status: JobRunning, Started: t0, Failing: first}
	stage := func(name, status string, keep bool, ended time.Duration, failures ...string) lane.Stage {
		return lane.Stage{Name: name, Status: status, KeepGoing: keep, Ended: t0.Add(ended), Failures: failures}
	}
	for _, c := range []struct {
		name string
		set  func(st *State)
		told string // the line it is told of; "" for none
	}{
		{"failing as it runs", func(*State) {}, first.Line},
		{"no failure yet", func(st *State) { st.Jobs[0].Failing = nil }, ""},
		{"told already", func(st *State) { st.Jobs[0].FailTold = t0.Add(9 * time.Minute) }, ""},
		{"ended", func(st *State) { st.Jobs[0].Status, st.Jobs[0].Ended = JobFailed, t0.Add(20*time.Minute) }, ""},
		{"an await", func(st *State) { st.Jobs[0].Kind = JobAwait }, first.Line},
		{"a compaction owed", func(st *State) { st.BoundaryOwed = true }, ""},
		{"a compaction under way", func(st *State) { st.Run.Compaction.Status = CompactActive }, ""},
		{"paused", func(st *State) { st.Mode = ModePaused }, ""},
		{"its stage runs on", func(st *State) { st.Jobs[0].Stages = []lane.Stage{stage("lint", lane.StagePassed, false, time.Minute)} }, first.Line},
		{"its stage ended without --keep-going", func(st *State) {
			st.Jobs[0].Stages = []lane.Stage{stage("e2e", lane.StageFailed, false, 9*time.Minute, "TestLogin")}
		}, first.Line},
		{"its stage ran with --keep-going", func(st *State) {
			st.Jobs[0].Stages = []lane.Stage{stage("e2e", lane.StageFailed, true, 9*time.Minute, "TestLogin")}
		}, ""},
		{"a later stage failed without --keep-going", func(st *State) {
			st.Jobs[0].Stages = []lane.Stage{stage("e2e", lane.StageFailed, true, 9*time.Minute, "TestLogin"),
				stage("perf", lane.StageFailed, false, 12*time.Minute, "BenchmarkRank"), stage("docs", lane.StageFailed, false, 13*time.Minute)}
		}, "BenchmarkRank"},
		{"a later stage failed with no ids", func(st *State) {
			st.Jobs[0].Stages = []lane.Stage{stage("e2e", lane.StageFailed, true, 9*time.Minute), stage("docs", lane.StageFailed, false, 13*time.Minute)}
		}, "stage docs failed"},
		{"an earlier stage of its name", func(st *State) {
			st.Jobs[0].Stages = []lane.Stage{stage("e2e", lane.StageFailed, true, 2*time.Minute)}
		}, first.Line},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := lookRun(gate)
			c.set(&st)
			due := st.UntoldFailures()
			if c.told == "" {
				if len(due) > 0 || st.LookInsDue() {
					t.Fatalf("told %+v", due)
				}
				return
			}
			if len(due) != 1 || due[0].ID != "j3" || due[0].Failing.Line != c.told {
				t.Fatalf("told %+v, want %q", due, c.told)
			}
			at := t0.Add(10 * time.Minute)
			failing, left := TakeLookIns(&st, at)
			if len(failing) != 1 || len(left) != 0 || failing[0].Failing.Line != c.told {
				t.Fatalf("took %+v %+v", failing, left)
			}
			if j := st.Job("j3"); !j.FailTold.Equal(at) || !slices.Equal(j.LookedIn, []string{LookFailing}) || j.Failing.Line != first.Line {
				t.Errorf("recorded %+v", j)
			}
			if st.LookInsDue() || len(st.UntoldFailures()) > 0 {
				t.Errorf("told twice")
			}
		})
	}
}

// A look-in the host left goes once per job and kind, to whatever phase is in hand; one about a job that
// ended since goes untold, and one about no job always goes.
func TestALookInGoesOncePerJobAndKind(t *testing.T) {
	gate := Job{ID: "j3", Phase: "P3", Kind: JobVerify, Command: "make e2e", Status: JobRunning, Started: t0}
	st := lookRun(gate)
	slow := LookIn{Kind: LookSlow, Job: "j3", Text: "slow", At: t0}
	if !AddLookIn(&st, slow) || AddLookIn(&st, slow) {
		t.Fatalf("left %+v", st.LookIns)
	}
	if !AddLookIn(&st, LookIn{Kind: LookTimeout, Job: "j3", Text: "late", At: t0}) {
		t.Fatal("another kind was not left")
	}
	if AddLookIn(&st, LookIn{Kind: LookSlow, Job: "j9", Text: "gone"}) {
		t.Error("left one about a job not on record")
	}
	if !slices.Equal(st.Job("j3").LookedIn, []string{LookSlow, LookTimeout}) || !st.LookInsDue() {
		t.Fatalf("job %+v, due %v", st.Job("j3"), st.LookInsDue())
	}
	st.BoundaryOwed = true
	if f, left := TakeLookIns(&st, t0); f != nil || left != nil || len(st.LookIns) != 2 {
		t.Fatalf("taken while compacting: %+v %+v", f, left)
	}
	st.BoundaryOwed = false
	if _, left := TakeLookIns(&st, t0); len(left) != 2 || len(st.LookIns) != 0 || st.LookInsDue() {
		t.Fatalf("took %+v, left %+v", left, st.LookIns)
	}
	if AddLookIn(&st, slow) {
		t.Error("left again once told")
	}

	AddLookIn(&st, LookIn{Kind: LookConflict, Text: "notes.md"})
	st.Jobs[0].Status = JobPassed
	if AddLookIn(&st, LookIn{Kind: LookHeld, Job: "j3"}) {
		t.Error("left one about a job that ended")
	}
	st.LookIns = append(st.LookIns, LookIn{Kind: LookHeld, Job: "j3", Text: "held"})
	if _, left := TakeLookIns(&st, t0); len(left) != 1 || left[0].Kind != LookConflict || len(st.LookIns) != 0 {
		t.Errorf("took %+v, left %+v", left, st.LookIns)
	}
}

// A file is told once, at its second landing conflict in the run; a third says nothing new.
func TestAFileInASecondLandingConflict(t *testing.T) {
	st := New()
	if got := CountConflict(&st, "P1", nil); got != nil || st.Conflicted != nil {
		t.Fatalf("no paths: %v %v", got, st.Conflicted)
	}
	for _, c := range []struct {
		phase  string
		paths  []string
		second []string
	}{
		{"P1", []string{"notes.md", "go.mod"}, nil},
		{"P2", []string{"notes.md"}, []string{"notes.md"}},
		{"P3", []string{"notes.md", "go.mod", "x.go"}, []string{"go.mod"}},
		{"P1", []string{"x.go"}, []string{"x.go"}},
	} {
		if got := CountConflict(&st, c.phase, c.paths); !slices.Equal(got, c.second) {
			t.Errorf("%s %v: second %v, want %v", c.phase, c.paths, got, c.second)
		}
	}
	want := map[string][]string{"notes.md": {"P1", "P2", "P3"}, "go.mod": {"P1", "P3"}, "x.go": {"P3", "P1"}}
	if !reflect.DeepEqual(st.Conflicted, want) {
		t.Errorf("conflicted %v", st.Conflicted)
	}
}

// The look-ins left and the conflicts counted read back as written, and a state with none writes neither.
func TestLookInsRoundTrip(t *testing.T) {
	st := New()
	st.LookIns = []LookIn{{Kind: LookHeld, Job: "j3", Text: "[baton] held", Say: "baton: held", At: t0}}
	st.Conflicted = map[string][]string{"notes.md": {"P1", "P2"}}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, st) {
		t.Errorf("read back %+v (%v)", back, err)
	}
	if b, _ := json.Marshal(New()); strings.Contains(string(b), "look_ins") || strings.Contains(string(b), "conflicted") {
		t.Errorf("a state with none writes them: %s", b)
	}
}

// The phases whose work waits to land, in roster order.
func TestLandingPhases(t *testing.T) {
	st := New()
	st.Workers = []Worker{{Phase: "P1", Status: WorkerLanding}, {Phase: "P2", Status: WorkerWorking}, {Phase: "P3", Status: WorkerLanding}}
	if got := st.LandingPhases(); !slices.Equal(got, []string{"P1", "P3"}) {
		t.Errorf("landing %v", got)
	}
}
