package state

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// graph builds a plan from "ID:deps" specs: "P2:P0,P1" waits for P0 and P1, "P0:" for nothing, and a bare
// "P3" has no After at all (a plan.json from before dependencies: it follows the phase before it).
func graph(specs ...string) plan.Plan {
	p := plan.Plan{Version: 1}
	for _, s := range specs {
		id, deps, found := strings.Cut(s, ":")
		ph := plan.Phase{ID: id, Title: "title " + id}
		if found {
			ph.After = []string{}
			if deps != "" {
				ph.After = strings.Split(deps, ",")
			}
		}
		p.Phases = append(p.Phases, ph)
	}
	return p
}

// runState is a run of p whose phases have the given statuses (pending when not named), parked with the
// given parks.
func runState(p plan.Plan, statuses map[string]string, parks map[string]Park, jobs ...Job) State {
	st := New()
	st.Mode = ModeRunning
	for _, ph := range p.Phases {
		ps := &PhaseState{Status: PhasePending}
		if s, ok := statuses[ph.ID]; ok {
			ps.Status = s
		}
		if pk, ok := parks[ph.ID]; ok {
			ps.Park = &pk
		}
		st.Phases[ph.ID] = ps
	}
	st.Jobs = jobs
	return st
}

func TestNext(t *testing.T) {
	chain := graph("P0:", "P1:P0", "P2:P1", "P3:P2")
	old := graph("P0", "P1", "P2") // no After stored
	diamond := graph("P0:", "P1:P0", "P2:P0", "P3:P1,P2")
	wide := graph("P0:", "P1:", "P2:", "P3:P0,P1,P2")
	running, passed, failed := Job{ID: "j1", Phase: "P0", Status: JobRunning}, Job{ID: "j1", Phase: "P0", Status: JobPassed}, Job{ID: "j1", Phase: "P0", Status: JobFailed}
	const done, active, verifying, parked = PhaseDone, PhaseActive, PhaseVerifying, PhaseParked
	for _, c := range []struct {
		name     string
		p        plan.Plan
		statuses map[string]string
		parks    map[string]Park
		jobs     []Job
		want     string
	}{
		{"a fresh chain starts at its first phase", chain, nil, nil, nil, "P0"},
		{"a chain goes in order", chain, map[string]string{"P0": done, "P1": done}, nil, nil, "P2"},
		{"a plan.json without dependencies goes in order", old, map[string]string{"P0": done}, nil, nil, "P1"},
		{"a phase finished out of order does not skip the one before it", old, map[string]string{"P0": done, "P2": done}, nil, nil, "P1"},
		{"a chain waits for the phase in hand", chain, map[string]string{"P0": active}, nil, nil, ""},
		{"a diamond's other branch can start while one is in hand", diamond, map[string]string{"P0": done, "P1": active}, nil, nil, "P2"},
		{"a diamond's join waits for both branches, so the other comes first", diamond, map[string]string{"P0": done, "P1": done}, nil, nil, "P2"},
		{"a verifying phase holds what builds on it", diamond, map[string]string{"P0": done, "P1": done, "P2": verifying}, nil, nil, ""},
		{"the join starts once both branches are done", diamond, map[string]string{"P0": done, "P1": done, "P2": done}, nil, nil, "P3"},
		{"a wide plan takes the first independent phase", wide, map[string]string{"P0": active}, nil, nil, "P1"},
		{"a phase parked while its job runs waits", wide, map[string]string{"P0": parked, "P1": done}, map[string]Park{"P0": {Reason: ParkJob, Job: "j1", Since: t0}}, []Job{running}, "P2"},
		{"a queued job holds its phase too", wide, map[string]string{"P0": parked, "P1": done, "P2": done}, map[string]Park{"P0": {Reason: ParkJob, Job: "j1", Since: t0}}, []Job{{ID: "j1", Status: JobQueued}}, ""},
		{"a parked phase whose job passed comes back first", wide, map[string]string{"P0": parked}, map[string]Park{"P0": {Reason: ParkJob, Job: "j1", Since: t0}}, []Job{passed}, "P0"},
		{"a parked phase whose job failed comes back first", wide, map[string]string{"P0": parked}, map[string]Park{"P0": {Reason: ParkJob, Job: "j1", Since: t0}}, []Job{failed}, "P0"},
		{"a phase reopened by a red verification comes back first", wide, map[string]string{"P0": parked}, map[string]Park{"P0": {Reason: ParkRed, Since: t0}}, nil, "P0"},
		{"a job no longer on record holds nothing", wide, map[string]string{"P0": parked}, map[string]Park{"P0": {Reason: ParkJob, Job: "j9", Since: t0}}, nil, "P0"},
		{"the phase parked longest comes back first", wide, map[string]string{"P0": parked, "P2": parked}, map[string]Park{
			"P0": {Reason: ParkRed, Since: t0.Add(time.Hour)},
			"P2": {Reason: ParkRed, Since: t0},
		}, nil, "P2"},
		{"a parked phase whose job runs does not come back", wide, map[string]string{"P0": parked, "P2": parked}, map[string]Park{
			"P0": {Reason: ParkJob, Job: "j1", Since: t0},
			"P2": {Reason: ParkRed, Since: t0.Add(time.Hour)},
		}, []Job{running}, "P2"},
		{"nothing is left", chain, map[string]string{"P0": done, "P1": done, "P2": done, "P3": done}, nil, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Next(runState(c.p, c.statuses, c.parks, c.jobs...), c.p); got != c.want {
				t.Errorf("Next = %q, want %q", got, c.want)
			}
		})
	}
}

func TestComplete(t *testing.T) {
	p := graph("P0:", "P1:")
	all := map[string]string{"P0": PhaseDone, "P1": PhaseDone}
	for _, c := range []struct {
		name     string
		statuses map[string]string
		jobs     []Job
		want     bool
	}{
		{"every phase done", all, nil, true},
		{"every phase done, with jobs finished", all, []Job{{ID: "j1", Status: JobPassed}, {ID: "j2", Status: JobInterrupted}}, true},
		{"a job still running", all, []Job{{ID: "j1", Status: JobPassed}, {ID: "j2", Status: JobRunning}}, false},
		{"a job still queued", all, []Job{{ID: "j1", Status: JobQueued}}, false},
		{"a phase verifying", map[string]string{"P0": PhaseDone, "P1": PhaseVerifying}, nil, false},
		{"a phase parked", map[string]string{"P0": PhaseDone, "P1": PhaseParked}, nil, false},
		{"a phase pending", map[string]string{"P0": PhaseDone}, nil, false},
	} {
		if got := Complete(runState(p, c.statuses, nil, c.jobs...), p); got != c.want {
			t.Errorf("%s: Complete = %v, want %v", c.name, got, c.want)
		}
	}
}

// A phase parked while its job runs: the run takes up an independent phase, holds nothing while only
// phases waiting on the job are left, and resumes the parked phase once the job is over.
func TestParkAndResume(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P0")
	dirt := &Dirt{Files: map[string]string{"b.go": "f2"}}
	st := Attach(p, t0, Origin{Head: "abc"})
	st.Touched("a.go")

	t1 := t0.Add(10 * time.Minute)
	st.Jobs = []Job{{ID: "j1", Phase: "P0", Status: JobRunning}}
	next, err := ParkPhase(&st, p, "P0", Park{Reason: ParkJob, Since: t1, Notes: "tests written; gate running", Job: "j1"})
	if err != nil || next != "P1" || st.Current != "P1" || !st.BoundaryOwed || st.Phases["P0"].Status != PhaseParked || st.Phases["P0"].Park.Notes != "tests written; gate running" {
		t.Fatalf("park P0: next %q err %v state %+v", next, err, st)
	}
	Start(&st, t1, Origin{Head: "def"})
	if next, err := Done(&st, p, "P1", t1, DoneOpts{}); err != nil || next != "" || st.Current != "" || st.BoundaryOwed || st.Mode != ModeRunning {
		t.Fatalf("done P1 while P0's job runs: next %q err %v state %+v", next, err, st)
	}

	st.Jobs[0].Status = JobPassed
	if next := advance(&st, p); next != "P0" || st.Current != "P0" || !st.BoundaryOwed {
		t.Fatalf("after the job: next %q state %+v", next, st)
	}
	if st.Resumed() {
		t.Fatal("P0 counts as resumed before its brief")
	}
	t2 := t1.Add(time.Hour)
	Start(&st, t2, Origin{Head: "ghi", Dirty: dirt})
	ps := st.Phases["P0"]
	if ps.Status != PhaseActive || ps.StartHead != "ghi" || ps.StartDirty != dirt || ps.Edited != nil || !ps.StartedAt.Equal(t0) || !ps.Park.Resumed.Equal(t2) || !st.Resumed() || st.BoundaryOwed {
		t.Fatalf("resumed P0: %+v park %+v", ps, ps.Park)
	}
	if next, err := Done(&st, p, "P0", t2, DoneOpts{}); err != nil || next != "P2" {
		t.Fatalf("done P0: next %q err %v", next, err)
	}
	Start(&st, t2, Origin{})
	if st.Resumed() {
		t.Fatal("P2 never parked, yet counts as resumed")
	}
}

func TestParkPhaseRefuses(t *testing.T) {
	p := graph("P0:", "P1:")
	st := Attach(p, t0, Origin{})
	idle := New()
	for name, err := range map[string]error{
		"no plan":   func() error { _, err := ParkPhase(&idle, p, "P0", Park{}); return err }(),
		"unknown":   func() error { _, err := ParkPhase(&st, p, "P9", Park{}); return err }(),
		"not begun": func() error { _, err := ParkPhase(&st, p, "P1", Park{}); return err }(),
		"done": func() error {
			st := Attach(p, t0, Origin{})
			Done(&st, p, "P0", t0, DoneOpts{})
			_, err := ParkPhase(&st, p, "P0", Park{})
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s: parked", name)
		}
	}
	// A phase not in hand (verifying) parks without moving the run.
	st.Phases["P1"].Status = PhaseVerifying
	if next, err := ParkPhase(&st, p, "P1", Park{Reason: ParkRed, Since: t0}); err != nil || next != "P0" || st.Current != "P0" || st.BoundaryOwed {
		t.Fatalf("park a verifying phase: next %q err %v state %+v", next, err, st)
	}
}

// Done with the phase's job still to finish makes it verifying, and the run moves on; what the job
// decides is applied by Settle. Phases that build on a verifying phase wait for it to pass.
func TestTwoStageDone(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P0")
	t1 := t0.Add(time.Hour)
	verifying := func(status string) State {
		st := Attach(p, t0, Origin{})
		st.Jobs = []Job{{ID: "j1", Phase: "P0", Status: JobRunning, Notes: "all written; make test running"}}
		if next, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || next != "P1" || st.Phases["P0"].Status != PhaseVerifying || !st.Phases["P0"].DoneAt.IsZero() {
			t.Fatalf("done P0 with its job running: next %q err %v state %+v", next, err, st)
		}
		if Unsettled(st) {
			t.Fatal("unsettled while the job runs")
		}
		if got := Settle(&st, p, t1); got.Changed() {
			t.Fatalf("settled while the job runs: %+v", got)
		}
		if status == "" {
			st.Jobs = nil
		} else {
			st.Jobs[0].Status = status
		}
		if status != JobRunning && !Unsettled(st) {
			t.Fatal("settled before Settle")
		}
		return st
	}
	for _, c := range []struct {
		status, phase, park string
		delivered           bool
	}{
		{JobPassed, PhaseDone, "", true},
		{JobFailed, PhaseParked, ParkRed, false},
		{JobTimeout, PhaseParked, ParkRed, false},
		{JobLaneError, PhaseParked, ParkUnverified, false},
		{JobInterrupted, PhaseParked, ParkUnverified, false},
		{"", PhaseParked, ParkUnverified, false}, // the job is gone from the record
	} {
		st := verifying(c.status)
		got := Settle(&st, p, t1)
		ps := st.Phases["P0"]
		if len(got.Verdicts) != 1 || got.Verdicts[0].Phase != "P0" || got.Verdicts[0].Park != c.park || got.Taken != "" || got.Complete || ps.Status != c.phase || st.Current != "P1" {
			t.Errorf("%q: settled %+v, P0 %+v, in hand %q", c.status, got, ps, st.Current)
			continue
		}
		if c.phase == PhaseDone && !ps.DoneAt.Equal(t1) {
			t.Errorf("%q: done at %v", c.status, ps.DoneAt)
		}
		if c.park != "" {
			if pk := ps.Park; pk == nil || pk.Reason != c.park || !pk.Since.Equal(t1) || c.status != "" && (pk.Job != "j1" || pk.Notes != "all written; make test running") {
				t.Errorf("%q: park %+v", c.status, pk)
			}
			// A reopened phase comes back before any phase not started, and what builds on it waits.
			Start(&st, t1, Origin{})
			if next, _ := Done(&st, p, "P1", t1, DoneOpts{}); next != "P0" {
				t.Errorf("%q: after P1, %q is taken up", c.status, next)
			}
		}
		if j := st.Job("j1"); j != nil && !j.Delivered.IsZero() != c.delivered {
			t.Errorf("%q: delivered %v", c.status, j.Delivered)
		}
		if Unsettled(st) {
			t.Errorf("%q: still unsettled", c.status)
		}
	}
	st := verifying(JobRunning)
	if Unsettled(st) || st.Phases["P2"].Status != PhasePending || Next(st, p) == "P2" {
		t.Fatalf("P2 started on a verifying P0: %+v", st)
	}
}

// With no phase in hand, a verdict lets the run take up what can start now, or completes it.
func TestSettleWithNothingInHand(t *testing.T) {
	t1 := t0.Add(time.Hour)
	waiting := func(p plan.Plan, status string) State {
		st := Attach(p, t0, Origin{})
		st.Jobs = []Job{{ID: "j1", Phase: "P0", Status: JobRunning}}
		if next, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || next != "" || st.Current != "" || st.Mode != ModeRunning {
			t.Fatalf("done P0: next %q err %v state %+v", next, err, st)
		}
		st.Jobs[0].Status = status
		return st
	}
	chain := graph("P0:", "P1:P0")
	st := waiting(chain, JobPassed)
	if got := Settle(&st, chain, t1); got.Taken != "P1" || got.Complete || st.Current != "P1" || !st.BoundaryOwed || st.Phases["P0"].Status != PhaseDone {
		t.Errorf("green: %+v, state %+v", got, st)
	}
	st = waiting(chain, JobFailed)
	if got := Settle(&st, chain, t1); got.Taken != "P0" || st.Current != "P0" || !st.BoundaryOwed || st.Phases["P0"].Status != PhaseParked {
		t.Errorf("red: %+v, state %+v", got, st)
	}

	// The verification that completes the run is how the model hears of it: its result stays due.
	one := graph("P0:")
	st = waiting(one, JobPassed)
	if got := Settle(&st, one, t1); got.Taken != "" || !got.Complete || st.Mode != ModeComplete {
		t.Fatalf("last phase green: %+v, state %+v", got, st)
	}
	if due := st.Due(); len(due) != 1 || due[0].ID != "j1" {
		t.Errorf("due once complete: %+v", due)
	}
	st.Run.CompleteNotified = true
	if due := st.Due(); len(due) != 0 {
		t.Errorf("due after the human was told: %+v", due)
	}

	// No phase in hand, and nothing left to wait for: the run is set right.
	st = runState(chain, map[string]string{"P0": PhaseDone}, nil)
	if !Unsettled(st) {
		t.Fatal("not unsettled")
	}
	if got := Settle(&st, chain, t1); got.Taken != "P1" || len(got.Verdicts) != 0 {
		t.Errorf("nothing to wait for: %+v", got)
	}
	// A parked phase whose job finished is taken up.
	st = runState(chain, map[string]string{"P0": PhaseParked}, map[string]Park{"P0": {Reason: ParkJob, Job: "j1"}}, Job{ID: "j1", Phase: "P0", Status: JobRunning})
	if Unsettled(st) {
		t.Fatal("unsettled while the job runs")
	}
	st.Jobs[0].Status = JobPassed
	if got := Settle(&st, chain, t1); got.Taken != "P0" || len(got.Verdicts) != 0 {
		t.Errorf("parked and resolved: %+v", got)
	}
}

// A phase whose latest job is red is not finished, unless the model says why it is despite that.
func TestDoneRefusesRed(t *testing.T) {
	p := graph("P0:", "P1:")
	run := func(jobs ...Job) State {
		st := Attach(p, t0, Origin{})
		st.Jobs = jobs
		return st
	}
	red := Job{ID: "j1", Phase: "P0", Command: "make test", Status: JobFailed, Exit: 1}
	st := run(red)
	_, err := Done(&st, p, "P0", t0, DoneOpts{})
	var re RedError
	if !errors.As(err, &re) || re.Job.ID != "j1" || st.Phases["P0"].Status != PhaseActive || !strings.Contains(err.Error(), "make test") {
		t.Fatalf("red: err %v, P0 %+v", err, st.Phases["P0"])
	}
	if next, err := Done(&st, p, "P0", t0, DoneOpts{DespiteRed: true}); err != nil || next != "P1" || st.Phases["P0"].Status != PhaseDone || st.Job("j1").Delivered.IsZero() {
		t.Fatalf("despite red: next %q err %v state %+v", next, err, st)
	}
	// Another phase's red, an older red, and a job with no verdict do not stand in the way.
	for name, jobs := range map[string][]Job{
		"another phase":  {{ID: "j1", Phase: "P1", Status: JobFailed}},
		"verified again": {red, {ID: "j2", Phase: "P0", Status: JobPassed}},
		"lane error":     {red, {ID: "j2", Phase: "P0", Status: JobLaneError}},
	} {
		st := run(jobs...)
		if _, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || st.Phases["P0"].Status != PhaseDone {
			t.Errorf("%s: err %v, P0 %+v", name, err, st.Phases["P0"])
		}
	}
	st = run(red, Job{ID: "j2", Phase: "P0", Status: JobQueued})
	if _, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || st.Phases["P0"].Status != PhaseVerifying {
		t.Errorf("verifying again: err %v, P0 %+v", err, st.Phases["P0"])
	}
}

func TestDue(t *testing.T) {
	p := graph("P0:", "P1:")
	t1 := t0.Add(time.Minute)
	finished := Job{ID: "j1", Phase: "P0", Status: JobFailed}
	for _, c := range []struct {
		name string
		set  func(*State)
		due  bool
	}{
		{"in hand", func(*State) {}, true},
		{"already given", func(st *State) { st.Jobs[0].Delivered = t0 }, false},
		{"still running", func(st *State) { st.Jobs[0].Status = JobRunning }, false},
		{"another phase's", func(st *State) { st.Jobs[0].Phase = "P1" }, false},
		{"boundary owed", func(st *State) { st.BoundaryOwed = true }, false},
		{"checkpoint owed", func(st *State) { st.CheckpointOwed = true }, false},
		{"checkpoint due", func(st *State) { st.CheckpointDue = true }, false},
		{"compacting", func(st *State) { st.Run.Compaction.Queue("checkpoint", t0) }, false},
		{"paused", func(st *State) { st.Mode = ModePaused }, false},
		{"phase parked", func(st *State) { st.Phases["P0"].Status = PhaseParked }, false},
		{"nothing in hand", func(st *State) { st.Current = "" }, false},
	} {
		st := Attach(p, t0, Origin{})
		st.Jobs = []Job{finished}
		c.set(&st)
		if due := st.Due(); (len(due) == 1) != c.due {
			t.Errorf("%s: due %+v", c.name, due)
		}
		if due := TakeDue(&st, t1); len(due) > 0 && (!st.Jobs[0].Delivered.Equal(t1) || len(st.Due()) != 0) {
			t.Errorf("%s: taken, but %+v", c.name, st.Jobs[0])
		}
	}
}

func TestRed(t *testing.T) {
	st := State{Jobs: []Job{
		{ID: "j1", Phase: "P0", Status: JobFailed},
		{ID: "j2", Phase: "P1", Status: JobTimeout},
		{ID: "j3", Phase: "P0", Status: JobPassed},
		{ID: "j4", Phase: "P2", Status: JobLaneError},
		{ID: "j5", Phase: "P3", Status: JobFailed},
		{ID: "j6", Phase: "P3", Status: JobRunning},
		{ID: "j7", Phase: "P4", Status: JobFailed},
	}}
	var ids []string
	for _, j := range st.Red() {
		ids = append(ids, j.ID)
	}
	if strings.Join(ids, " ") != "j2 j7" {
		t.Errorf("red: %v", ids)
	}
}

func TestReviseKeepsParkedAndVerifyingPhases(t *testing.T) {
	p := graph("P0:", "P1:", "P2:", "P3:P1")
	running := func() State {
		st := runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseVerifying, "P2": PhaseParked, "P3": PhasePending},
			map[string]Park{"P2": {Reason: ParkJob, Job: "j2", Since: t0, Notes: "half way"}}, Job{ID: "j1", Phase: "P1", Status: JobRunning}, Job{ID: "j2", Phase: "P2", Status: JobRunning})
		return st
	}
	withNew := graph("P0:", "P1:", "P2:", "P3:P1", "P4:")
	st := running()
	if next := Revise(&st, withNew, t0); next != "P4" || !st.BoundaryOwed || st.Phases["P1"].Status != PhaseVerifying || st.Phases["P2"].Status != PhaseParked || st.Phases["P2"].Park.Notes != "half way" || st.Phases["P0"].Status != PhaseDone {
		t.Fatalf("revision adding P4: next %q state %+v", next, st)
	}
	st = running()
	if next := Revise(&st, graph("P0:", "P1:", "P3:P1"), t0); next != "" || st.Mode != ModeRunning || st.Current != "" || st.Phases["P2"] != nil {
		t.Fatalf("revision dropping P2, with P3 waiting on P1's verification: next %q state %+v", next, st)
	}
	if j1, j2 := st.Job("j1"), st.Job("j2"); j1.Status != JobRunning || j2.Status != JobInterrupted || j2.Reason != "its phase is no longer in the plan" || !j2.Ended.Equal(t0) {
		t.Fatalf("the jobs after P2 was dropped: %+v, %+v", *j1, *j2)
	}

	// The phase under way carries on when its dependencies are done, and starts over after a compaction
	// when the revision makes it wait for a phase not done.
	st = runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseVerifying, "P2": PhaseActive}, nil)
	st.Current = "P2"
	if next := Revise(&st, p, t0); next != "P2" || st.BoundaryOwed || st.Phases["P2"].Status != PhaseActive {
		t.Fatalf("P2 kept: next %q state %+v", next, st)
	}
	st = runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseVerifying, "P2": PhaseActive}, nil)
	st.Current = "P2"
	if next := Revise(&st, graph("P0:", "P1:", "P2:P1", "P3:P1", "P4:P0"), t0); next != "P4" || !st.BoundaryOwed || st.Phases["P2"].Status != PhasePending {
		t.Fatalf("P2 now waits for P1's verification: next %q state %+v", next, st)
	}
}

func TestQueueJob(t *testing.T) {
	var st State
	q := func(phase string) (Job, *Job, []Job) {
		return QueueJob(&st, Job{Phase: phase, Kind: JobVerify, Command: "make test"}, t0)
	}
	if j, old, dropped := q("P0"); j.ID != "j1" || j.Status != JobQueued || !j.Queued.Equal(t0) || old != nil || dropped != nil || len(st.Jobs) != 1 {
		t.Fatalf("first job: %+v, replaced %v, dropped %v", j, old, dropped)
	}
	st.Jobs[0].Status = JobRunning
	q("P1")
	// A phase has one job at a time: a new one replaces it.
	j, old, _ := q("P0")
	if j.ID != "j3" || old == nil || old.ID != "j1" || old.Status != JobRunning {
		t.Fatalf("the replacement %+v of %+v", j, old)
	}
	if j1 := st.Job("j1"); j1.Status != JobInterrupted || j1.Reason != "replaced by j3" || j1.Exit != -1 || !j1.Ended.Equal(t0) || !j1.Delivered.Equal(t0) {
		t.Errorf("the replaced job: %+v", *j1)
	}
	if j2 := st.Job("j2"); j2.Status != JobQueued {
		t.Errorf("another phase's job: %+v", *j2)
	}
	if got := st.Unfinished(); len(got) != 2 || got[0].ID != "j2" || got[1].ID != "j3" {
		t.Errorf("unfinished: %+v", got)
	}

	// The record keeps MaxJobs, dropping the oldest finished jobs and never one still to finish.
	st = State{Jobs: []Job{{ID: "j1", Phase: "P0", Status: JobRunning}}}
	for i := 2; i <= MaxJobs; i++ {
		st.Jobs = append(st.Jobs, Job{ID: fmt.Sprintf("j%d", i), Phase: "P1", Status: JobPassed})
	}
	j, _, dropped := q("P2")
	if j.ID != "j31" || len(st.Jobs) != MaxJobs || len(dropped) != 1 || dropped[0].ID != "j2" || st.Jobs[0].ID != "j1" {
		t.Fatalf("job %s, dropped %+v, kept %d from %s", j.ID, dropped, len(st.Jobs), st.Jobs[0].ID)
	}
}

// Job ids are never used twice in a run, whatever became of the plan the jobs ran for: a runner still
// winding down must not take a later job for its own.
func TestJobIdsOutliveThePlan(t *testing.T) {
	p := graph("P0:", "P1:")
	st := Attach(p, t0, Origin{})
	QueueJob(&st, Job{Phase: "P0"}, t0)
	st = Reattach(st, p, t0, Origin{})
	if j, _, _ := QueueJob(&st, Job{Phase: "P0"}, t0); j.ID != "j2" {
		t.Errorf("after a new plan: %s", j.ID)
	}
	st = AttachAtBoundary(st, p, t0)
	if j, _, _ := QueueJob(&st, Job{Phase: "P0"}, t0); j.ID != "j3" {
		t.Errorf("after a plan approved at a boundary: %s", j.ID)
	}
	Detach(&st)
	if len(st.Jobs) != 0 {
		t.Errorf("a dropped plan keeps its jobs: %+v", st.Jobs)
	}
	st = Reattach(st, p, t0, Origin{})
	if j, _, _ := QueueJob(&st, Job{Phase: "P0"}, t0); j.ID != "j4" {
		t.Errorf("after a drop: %s", j.ID)
	}
}
