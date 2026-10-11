package state

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// worker is the roster entry of a worker on phase, as the host assigns it.
func worker(phase, session string) Worker {
	return Worker{Phase: phase, Ref: WorkerRef("r1", phase), Session: session, Instance: WorkerInstance("i1", phase),
		Worktree: "/worktrees/" + phase, Branch: "baton/r1/" + phase, Base: "abc"}
}

// assigned is a running run of p in which the lead holds lead (active, or "" for none), the phases have
// the given statuses (pending when not named), and each phase in workers is assigned to a worker.
func assigned(t *testing.T, p plan.Plan, lead string, statuses map[string]string, workers ...string) State {
	t.Helper()
	st := runState(p, statuses, nil)
	st.Current = lead
	if lead != "" {
		st.Phases[lead].Status = PhaseActive
	}
	for _, id := range workers {
		if err := Assign(&st, p, worker(id, "s-"+id), t0); err != nil {
			t.Fatalf("assign %s: %v", id, err)
		}
	}
	return st
}

// A worker takes a phase that can go ahead and is not the lead's; a working phase is never taken up
// again, by a worker or the lead, and what needs it waits until it has landed.
func TestNextWorker(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P0,P1", "P3:")
	const done, active, verifying, parked, working = PhaseDone, PhaseActive, PhaseVerifying, PhaseParked, PhaseWorking
	for _, c := range []struct {
		name       string
		lead       string
		statuses   map[string]string
		next, want string // the lead's next phase (Next), and a worker's (NextWorker)
	}{
		{"the lead holds P0: a worker takes P1", "P0", map[string]string{"P0": active}, "P1", "P1"},
		{"a working phase is never taken up again", "P0", map[string]string{"P0": active, "P1": working}, "P3", "P3"},
		{"what needs a working phase waits", "", map[string]string{"P0": done, "P1": working, "P3": working}, "", ""},
		{"once it has landed, the lead takes up what needed it", "", map[string]string{"P0": done, "P1": done, "P3": working}, "P2", ""},
		{"with nothing in hand, the lead's next phase is its own", "", map[string]string{"P0": done}, "P1", "P3"},
		{"a phase the lead starts after a compaction is its own", "P1", map[string]string{"P0": done, "P1": PhasePending}, "P1", "P3"},
		{"a parked phase is the lead's", "P1", map[string]string{"P0": parked, "P1": active}, "P0", "P3"},
		{"a parked phase is the lead's, even with nothing else to take", "P1", map[string]string{"P0": parked, "P1": active, "P3": working}, "P0", ""},
		{"a verifying phase is not done", "P3", map[string]string{"P0": verifying, "P1": done, "P3": active}, "", ""},
	} {
		st := runState(p, c.statuses, map[string]Park{"P0": {Reason: ParkCheckpoint, Notes: "half way"}})
		st.Current = c.lead
		if got := Next(st, p); got != c.next {
			t.Errorf("%s: Next = %q, want %q", c.name, got, c.next)
		}
		if got := NextWorker(st, p); got != c.want {
			t.Errorf("%s: NextWorker = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAssign(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P1")
	st := assigned(t, p, "P0", nil)
	t1 := t0.Add(time.Minute)
	if err := Assign(&st, p, worker("P1", "s1"), t1); err != nil {
		t.Fatal(err)
	}
	ps, w := st.Phases["P1"], st.WorkerOn("P1")
	if ps.Status != PhaseWorking || ps.Worker != "r1/P1" || !ps.StartedAt.Equal(t1) {
		t.Errorf("the phase: %+v", ps)
	}
	if w == nil || w.Status != WorkerStarting || w.Session != "s1" || !w.Assigned.Equal(t1) || !w.Since.Equal(t1) || w.Branch != "baton/r1/P1" {
		t.Fatalf("the roster: %+v", st.Workers)
	}
	if err := Assign(&st, p, worker("P1", "s1"), t1.Add(time.Minute)); err != nil || len(st.Workers) != 1 || !st.Workers[0].Assigned.Equal(t1) {
		t.Errorf("assigned again to the same worker: %v, %+v", err, st.Workers)
	}
	if got := st.LiveWorkers(); len(got) != 1 || got[0].Phase != "P1" {
		t.Errorf("live workers: %+v", got)
	}

	for _, c := range []struct {
		name string
		edit func(*State)
		w    Worker
		want string
	}{
		{"a paused run", func(st *State) { st.Mode = ModePaused }, worker("P2", "s2"), "while a plan runs"},
		{"an unknown phase", nil, worker("P9", "s2"), `unknown phase "P9"`},
		{"no session", nil, worker("P2", ""), "needs its sub-run and its session"},
		{"another worker's phase", nil, worker("P1", "s2"), "has a worker already"},
		{"the lead's phase", nil, worker("P0", "s2"), "is the lead's"},
		{"a phase that waits for a working one", nil, worker("P2", "s2"), "waits for phases not done yet"},
		{"a phase done", func(st *State) { st.Phases["P2"].Status, st.Phases["P1"].Status = PhaseDone, PhaseDone }, worker("P2", "s2"), "is done, not pending"},
	} {
		cp := assigned(t, p, "P0", nil, "P1")
		if c.edit != nil {
			c.edit(&cp)
		}
		before := len(cp.Workers)
		if err := Assign(&cp, p, c.w, t1); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
		if len(cp.Workers) != before {
			t.Errorf("%s: the roster grew: %+v", c.name, cp.Workers)
		}
	}

	// The roster is the run's state: it is saved and read back whole.
	s, err := Open(filepath.Join(t.TempDir(), DirName), "i1", func() time.Time { return t1 })
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Update(func(n *State) error { *n = st; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if back, err := s.Load(); err != nil || !reflect.DeepEqual(back.Workers, saved.Workers) || back.Phases["P1"].Worker != "r1/P1" {
		t.Errorf("read back: %+v (%v)", back.Workers, err)
	}
}

// A phase is done once its work lands, not when its worker finishes: what needs it waits until then,
// and a lead with nothing in hand takes it up, after a compaction, as at a phase boundary.
func TestLanded(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P0,P1")
	st := assigned(t, p, "P0", nil, "P1")
	if next, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || next != "" || st.Current != "" || st.Mode != ModeRunning || st.BoundaryOwed {
		t.Fatalf("done P0 while P1 works: next %q err %v state %+v", next, err, st)
	}
	t1 := t0.Add(time.Hour)
	st.Conflicts = 2
	l := Landing{Phase: "P1", Commits: []string{"c1", "c2"}, Files: []string{"b.go"}, Head: "c2"}
	if next, err := Landed(&st, p, l, t1); err != nil || next != "P2" || st.Current != "P2" || !st.BoundaryOwed {
		t.Fatalf("P1 landed: next %q err %v state %+v", next, err, st)
	}
	if st.Conflicts != 0 {
		t.Errorf("the landings that conflicted before still count: %d", st.Conflicts)
	}
	if c := st.Run.Compaction; c.Status != CompactQueued || c.Reason != "boundary" || !c.Queued.Equal(t1) {
		t.Errorf("the boundary compaction: %+v", c)
	}
	if ps := st.Phases["P1"]; ps.Status != PhaseDone || !ps.DoneAt.Equal(t1) || ps.Worker != "r1/P1" {
		t.Errorf("the landed phase: %+v", ps)
	}
	if w := st.Workers[0]; w.Status != WorkerLanded || !w.Since.Equal(t1) || st.WorkerOn("P1") != nil || len(st.LiveWorkers()) != 0 {
		t.Errorf("the roster: %+v", st.Workers)
	}
	if len(st.Landed) != 1 || !st.Landed[0].At.Equal(t1) || st.Landed[0].Head != "c2" || len(st.Landed[0].Commits) != 2 {
		t.Errorf("the landings: %+v", st.Landed)
	}
	epoch := st.Run.Compaction.Epoch
	if next, err := Landed(&st, p, l, t1.Add(time.Minute)); err != nil || next != "" || len(st.Landed) != 1 || st.Run.Compaction.Epoch != epoch {
		t.Errorf("landed again: next %q err %v, %d landings, epoch %d", next, err, len(st.Landed), st.Run.Compaction.Epoch)
	}

	for _, c := range []struct {
		name     string
		p        plan.Plan
		lead     string
		statuses map[string]string
		jobs     []Job
		mode     string
		next     string
		current  string
		complete bool
	}{
		{"the lead holds a phase: it carries on", p, "P0", nil, nil, ModeRunning, "", "P0", false},
		{"the last phase lands: the run is complete", graph("P0:", "P1:"), "", map[string]string{"P0": PhaseDone}, nil, ModeRunning, "", "", true},
		{"a job still runs: the run waits for it", graph("P0:", "P1:"), "", map[string]string{"P0": PhaseVerifying}, []Job{{ID: "j1", Phase: "P0", Status: JobRunning}}, ModeRunning, "", "", false},
		{"another phase still works: the run waits for it", graph("P0:", "P1:", "P2:"), "", map[string]string{"P0": PhaseDone}, nil, ModeRunning, "", "", false},
		{"the run is paused: the lead takes up nothing", p, "", map[string]string{"P0": PhaseDone}, nil, ModePaused, "", "", false},
	} {
		workers := []string{"P1"}
		if c.p.Index("P2") >= 0 && len(c.p.Deps("P2")) == 0 {
			workers = append(workers, "P2")
		}
		st := assigned(t, c.p, c.lead, c.statuses, workers...)
		st.Jobs, st.Mode = c.jobs, c.mode
		next, err := Landed(&st, c.p, Landing{Phase: "P1", Commits: []string{"c1"}, Head: "c1"}, t1)
		if err != nil || next != c.next || st.Current != c.current || (st.Mode == ModeComplete) != c.complete || st.Phases["P1"].Status != PhaseDone {
			t.Errorf("%s: next %q err %v, state %+v", c.name, next, err, st)
		}
		if st.Run.Compaction.Status == CompactQueued {
			t.Errorf("%s: a compaction was queued", c.name)
		}
	}

	st = assigned(t, p, "P0", nil)
	if _, err := Landed(&st, p, Landing{Phase: "P1"}, t1); err == nil || len(st.Landed) != 0 || st.Phases["P1"].Status != PhasePending {
		t.Errorf("a phase no worker has landed: %v, %+v", err, st)
	}
}

// The plan is complete only once no phase is working, whatever the lead has finished.
func TestCompletionWaitsForWorkingPhases(t *testing.T) {
	p := graph("P0:", "P1:")
	if Complete(runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseWorking}, nil), p) {
		t.Error("complete with a phase working")
	}
	st := assigned(t, p, "P0", nil, "P1")
	if next, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil || next != "" || st.Mode != ModeRunning || st.Current != "" {
		t.Fatalf("the lead's last phase done: next %q err %v state %+v", next, err, st)
	}
	if Unsettled(st) {
		if got := Settle(&st, p, t0); got.Complete || st.Mode == ModeComplete {
			t.Fatalf("settled to complete with P1 working: %+v", got)
		}
	}
	if _, err := Landed(&st, p, Landing{Phase: "P1", Head: "c1"}, t0); err != nil || st.Mode != ModeComplete {
		t.Errorf("P1 landed: %v, mode %s", err, st.Mode)
	}
}

// A worker's phase is not the lead's to finish or set aside.
func TestTheLeadCannotFinishAWorkersPhase(t *testing.T) {
	p := graph("P0:", "P1:")
	st := assigned(t, p, "P0", nil, "P1")
	if _, err := Done(&st, p, "P1", t0, DoneOpts{Force: true}); err == nil || !strings.Contains(err.Error(), "a worker's") || st.Phases["P1"].Status != PhaseWorking {
		t.Errorf("done --force: %v, %+v", err, st.Phases["P1"])
	}
	if _, err := ParkPhase(&st, p, "P1", Park{Reason: ParkCheckpoint, Since: t0}); err == nil || !strings.Contains(err.Error(), "a worker's") || st.Phases["P1"].Status != PhaseWorking {
		t.Errorf("parked: %v, %+v", err, st.Phases["P1"])
	}
}

// A worker that cannot go on hands its phase back: the phase is pending again, for the lead (when it
// holds none) or another worker.
func TestUnassign(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P1")
	t1 := t0.Add(time.Hour)
	st := assigned(t, p, "P0", nil, "P1")
	if next, err := Unassign(&st, p, "P1", "its session ended twice", t1); err != nil || next != "" || st.Current != "P0" || st.BoundaryOwed {
		t.Fatalf("with the lead busy: next %q err %v state %+v", next, err, st)
	}
	if ps := st.Phases["P1"]; ps.Status != PhasePending || ps.Worker != "" {
		t.Errorf("the phase: %+v", ps)
	}
	if w := st.Workers[0]; w.Status != WorkerFailed || w.Why != "its session ended twice" || !w.Since.Equal(t1) || st.WorkerOn("P1") != nil {
		t.Errorf("the roster: %+v", st.Workers)
	}
	if got := NextWorker(st, p); got != "P1" {
		t.Errorf("NextWorker after: %q", got)
	}
	if err := Assign(&st, p, worker("P1", "s2"), t1); err != nil || len(st.Workers) != 2 || st.WorkerOn("P1").Session != "s2" {
		t.Errorf("assigned to another worker: %v, %+v", err, st.Workers)
	}
	if _, err := Unassign(&st, p, "P0", "nothing", t1); err == nil {
		t.Error("unassigned the lead's phase")
	}

	// With nothing in hand, the lead takes the phase up.
	st = assigned(t, p, "", map[string]string{"P0": PhaseDone}, "P1")
	if next, err := Unassign(&st, p, "P1", "its worktree is gone", t1); err != nil || next != "P1" || st.Current != "P1" || !st.BoundaryOwed || st.Run.Compaction.Status != CompactQueued {
		t.Errorf("with the lead idle: next %q err %v state %+v", next, err, st)
	}

	// A phase the plan no longer has: its worker is done for, and nothing else changes.
	st = assigned(t, p, "P0", nil, "P1")
	Revise(&st, graph("P0:", "P2:"), t0)
	if next, err := Unassign(&st, graph("P0:", "P2:"), "P1", "its phase is no longer in the plan", t1); err != nil || next != "" || st.Phases["P1"] != nil || st.Workers[0].Status != WorkerFailed {
		t.Errorf("a phase removed: next %q err %v state %+v", next, err, st)
	}
}

func TestReviseKeepsWorkingPhases(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P1")
	st := assigned(t, p, "P0", nil, "P1")
	if next := Revise(&st, graph("P0:", "P1:", "P2:P1", "P3:"), t0); next != "P0" || st.Current != "P0" || st.BoundaryOwed {
		t.Fatalf("a phase added: next %q state %+v", next, st)
	}
	if ps := st.Phases["P1"]; ps.Status != PhaseWorking || ps.Worker != "r1/P1" || st.WorkerOn("P1") == nil {
		t.Errorf("the working phase: %+v", ps)
	}
	if got := NextWorker(st, graph("P0:", "P1:", "P2:P1", "P3:")); got != "P3" {
		t.Errorf("NextWorker: %q", got)
	}

	// The lead holds nothing, and the revision adds nothing it can start: it still waits for P1.
	st = assigned(t, p, "", map[string]string{"P0": PhaseDone}, "P1")
	if next := Revise(&st, p, t0); next != "" || st.Current != "" || st.Mode != ModeRunning || st.Phases["P1"].Status != PhaseWorking {
		t.Errorf("waiting on P1: next %q state %+v", next, st)
	}

	// A working phase the revision leaves out is gone from the run; the host ends its worker.
	st = assigned(t, p, "P0", nil, "P1")
	Revise(&st, graph("P0:", "P2:"), t0)
	if st.Phases["P1"] != nil || st.WorkerOn("P1") == nil {
		t.Errorf("P1 dropped: %+v, roster %+v", st.Phases, st.Workers)
	}
}

// A worker's sub-run numbers its decisions and jobs under its phase, so no id is used twice across the
// run.
func TestIDsAreUniqueAcrossSubRuns(t *testing.T) {
	p := graph("P0:", "P1:", "P2:", "P3:")
	lead := assigned(t, p, "P0", nil, "P2", "P3")
	subs := map[string]*State{}
	for _, phase := range []string{"P2", "P3"} {
		sub := AttachWorker(New(), phase)
		if sub.Mode != ModeRunning || sub.Current != phase || sub.Phases[phase].Status != PhasePending || sub.BoundaryOwed {
			t.Fatalf("%s's sub-run: %+v", phase, sub)
		}
		Start(&sub, t0, Origin{Head: "abc"})
		subs[phase] = &sub
	}
	seen := map[string]string{}
	use := func(where, id string) {
		if prev, ok := seen[id]; ok {
			t.Errorf("id %s is %s's and %s's", id, prev, where)
		}
		seen[id] = where
	}
	for _, c := range []struct {
		where      string
		st         *State
		dids, jids []string
	}{
		{"the lead", &lead, []string{"d1", "d2"}, []string{"j1", "j2"}},
		{"P2", subs["P2"], []string{"P2.d1", "P2.d2"}, []string{"P2.j1", "P2.j2"}},
		{"P3", subs["P3"], []string{"P3.d1", "P3.d2"}, []string{"P3.j1", "P3.j2"}},
	} {
		for i := range 2 {
			d, err := AddNote(c.st, "a note", "", t0)
			if err != nil || d.ID != c.dids[i] {
				t.Errorf("%s: decision %q (%v), want %q", c.where, d.ID, err, c.dids[i])
			}
			use(c.where, d.ID)
			j, _, _ := QueueJob(c.st, Job{Phase: c.st.Current, Kind: JobVerify, Command: "make test"}, t0)
			if j.ID != c.jids[i] {
				t.Errorf("%s: job %q, want %q", c.where, j.ID, c.jids[i])
			}
			use(c.where, j.ID)
		}
	}

	// Numbering goes on after the jobs on record, and outlives them, as in any run.
	sub := subs["P3"]
	sub.Jobs = append(sub.Jobs, Job{ID: "P3.j7", Phase: "P3", Status: JobPassed})
	if j, _, _ := QueueJob(sub, Job{Phase: "P3"}, t0); j.ID != "P3.j8" {
		t.Errorf("after P3.j7: %s", j.ID)
	}
	again := AttachWorker(*sub, "P3")
	if j, _, _ := QueueJob(&again, Job{Phase: "P3"}, t0); j.ID != "P3.j9" {
		t.Errorf("a sub-run started again: %s", j.ID)
	}
	Detach(sub)
	if j, _, _ := QueueJob(sub, Job{Phase: "P3"}, t0); j.ID != "P3.j9" {
		t.Errorf("after a drop: %s", j.ID)
	}
	if st := Reattach(*sub, graph("P3:"), t0, Origin{}); st.IDPrefix != "P3." {
		t.Errorf("a new plan for the sub-run: prefix %q", st.IDPrefix)
	}
}

// Each plan attached to a run is a generation of it. A new plan keeps the workers the run still counts
// on, as the replaced plan's, and drops the rest; a worker belongs to the generation it was assigned
// under, and is never the worker on a phase of a later plan, which may reuse its phase's id.
func TestPlanGenerations(t *testing.T) {
	p := graph("P0:", "P1:", "P2:", "P3:")
	st := Attach(p, t0, Origin{})
	if st.Generation != 1 {
		t.Fatalf("a first plan is generation %d", st.Generation)
	}
	for _, id := range []string{"P1", "P2", "P3"} {
		if err := Assign(&st, p, worker(id, "s-"+id), t0); err != nil {
			t.Fatal(err)
		}
	}
	if w := st.WorkerOn("P1"); w == nil || w.Generation != 1 {
		t.Fatalf("P1's worker: %+v", w)
	}
	if _, err := Landed(&st, p, Landing{Phase: "P2", Commits: []string{"c2"}}, t0); err != nil {
		t.Fatal(err)
	}
	Unassign(&st, p, "P3", "it failed", t0)

	next := Reattach(st, p, t0.Add(time.Hour), Origin{})
	if next.Generation != 2 || len(next.Workers) != 1 || next.Workers[0].Phase != "P1" || next.Workers[0].Status != WorkerStarting {
		t.Fatalf("generation %d, roster %+v: want P1's worker alone kept", next.Generation, next.Workers)
	}
	if AttachAtBoundary(st, p, t0).Generation != 2 {
		t.Error("a plan attached at a boundary is not a generation")
	}
	if w := next.WorkerOn("P1"); w != nil {
		t.Errorf("the replaced plan's worker counts as P1's: %+v", w)
	}
	if ps := next.Phases["P1"]; ps.Status != PhasePending {
		t.Errorf("the new plan's P1: %+v", ps)
	}
	// Until the host has retired it, no worker of the new plan takes up P1: it would share its sub-run,
	// its worktree and its branch. The lead may.
	if got := NextWorker(next, p); got != "P2" {
		t.Errorf("NextWorker = %q, want P2", got)
	}
	if err := Assign(&next, p, worker("P1", "s-new"), t0); err == nil || !strings.Contains(err.Error(), "not retired yet") {
		t.Errorf("assigned P1 beside the replaced plan's worker: %v", err)
	}
	ReleaseWorker(&next, "r1/P1", "s-P1", "the run's plan was replaced", t0)
	if err := Assign(&next, p, worker("P1", "s-new"), t0); err != nil {
		t.Fatal(err)
	}
	if w := next.WorkerOn("P1"); w == nil || w.Session != "s-new" || w.Generation != 2 {
		t.Errorf("P1's worker: %+v", w)
	}
}

// ReleaseWorker frees a worker's phase for a process that is not the run's host: the worker has failed,
// its phase is pending, and nothing else of the run changes, not the phase in hand nor a compaction,
// which the run's next host sees to.
func TestReleaseWorker(t *testing.T) {
	p := graph("P0:", "P1:", "P2:P1")
	t1 := t0.Add(time.Hour)
	for _, c := range []struct {
		name    string
		lead    string
		before  func(*State)
		ref     string
		session string
		ok      bool
		phase   string // P1's status after
	}{
		{"the lead busy", "P0", nil, "r1/P1", "s-P1", true, PhasePending},
		{"the lead idle: it takes nothing up", "", func(st *State) { st.Phases["P0"].Status = PhaseDone }, "r1/P1", "s-P1", true, PhasePending},
		{"another session's worker", "P0", nil, "r1/P1", "s-other", false, PhaseWorking},
		{"a failed worker", "P0", func(st *State) { st.Workers[0].Status = WorkerFailed }, "r1/P1", "s-P1", false, PhaseWorking},
		{"a replaced plan's worker", "P0", func(st *State) { st.Generation++ }, "r1/P1", "s-P1", true, PhaseWorking},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := assigned(t, p, c.lead, nil, "P1")
			if c.before != nil {
				c.before(&st)
			}
			compaction, current := st.Run.Compaction, st.Current
			if got := ReleaseWorker(&st, c.ref, c.session, "a new run started in this project", t1); got != c.ok {
				t.Errorf("ReleaseWorker = %v, want %v", got, c.ok)
			}
			if st.Phases["P1"].Status != c.phase || st.Current != current || st.BoundaryOwed || !reflect.DeepEqual(st.Run.Compaction, compaction) {
				t.Errorf("P1 %+v, current %q (was %q), boundary owed %v, compaction %+v", st.Phases["P1"], st.Current, current, st.BoundaryOwed, st.Run.Compaction)
			}
			if w := st.Workers[0]; c.ok && (w.Status != WorkerFailed || w.Why != "a new run started in this project" || !w.Since.Equal(t1)) {
				t.Errorf("the worker: %+v", w)
			}
		})
	}
}

// A run is abandoned once nothing drives it: no fresh heartbeat, no baton process left of its owner, and
// no sign of life from it or its worker for AbandonAfter.
func TestAbandoned(t *testing.T) {
	now := t0.Add(time.Hour)
	stale := &Owner{Instance: "i1", PID: 4242, Heartbeat: now.Add(-time.Hour)}
	for _, c := range []struct {
		name    string
		owner   *Owner
		updated time.Time
		e       *Worker
		baton   bool          // the owner's pid is a running baton
		quiet   time.Duration // AbandonAfter if 0
		want    bool
	}{
		{"a live heartbeat", &Owner{Instance: "i1", PID: 4242, Heartbeat: now.Add(-10 * time.Second)}, now.Add(-time.Hour), nil, false, 0, false},
		{"a stale heartbeat, quiet for long", stale, now.Add(-time.Hour), nil, false, 0, true},
		{"its owner's baton still runs (the machine slept)", stale, now.Add(-time.Hour), nil, true, 0, false},
		{"no owner on record", nil, now.Add(-AbandonAfter), nil, false, 0, true},
		{"its state changed a minute ago (a restart)", stale, now.Add(-time.Minute), nil, false, 0, false},
		{"its worker ended a minute ago", stale, now.Add(-time.Hour), &Worker{Since: now.Add(-time.Hour), Ended: now.Add(-time.Minute)}, false, 0, false},
		{"its worker was suspended a minute ago", stale, now.Add(-time.Hour), &Worker{Since: now.Add(-time.Minute)}, false, 0, false},
		{"its worker quiet for long", stale, now.Add(-time.Hour), &Worker{Since: now.Add(-time.Hour), Ended: now.Add(-time.Hour)}, false, 0, true},
		{"quiet for longer than a test asks", stale, now.Add(-10 * time.Second), nil, false, 5 * time.Second, true},
		{"its worker quiet for less than a test asks", stale, now.Add(-time.Hour), &Worker{Since: now.Add(-time.Second)}, false, 5 * time.Second, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := RunInfo{ID: "r1", State: State{Owner: c.owner}, Updated: c.updated}
			var asked int
			baton := func(pid int) bool { asked = pid; return c.baton }
			quiet := c.quiet
			if quiet == 0 {
				quiet = AbandonAfter
			}
			if got := Abandoned(r, c.e, baton, now, quiet); got != c.want {
				t.Errorf("Abandoned = %v, want %v", got, c.want)
			}
			if c.owner != nil && !c.owner.Live(now) && asked != c.owner.PID {
				t.Errorf("the owner's pid was not looked up (%d)", asked)
			}
		})
	}
}
