package state

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// A phase runs one job of each kind at a time: an await queued while a verify runs leaves it running, and
// the reverse. Within a kind, only the same check again replaces silently; another clashes, and replaces
// the one that runs only when the caller queues it anyway (--replace).
func TestOneJobOfEachKind(t *testing.T) {
	gate := Job{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "make qa", Commit: "c1", Status: JobRunning}
	watch := Job{ID: "j1", Phase: "P0", Kind: JobAwait, Command: "gh run watch 7 --exit-status", Status: JobRunning}
	for _, c := range []struct {
		name     string
		old      Job
		new      Job
		clash    bool // Clash names the old job
		replaced bool // queued, it stops the old job
		beside   bool // and runs beside it
	}{
		{"an await beside a running verify", gate, Job{Phase: "P0", Kind: JobAwait, Command: "gh run watch 7 --exit-status"}, false, false, true},
		{"a verify beside a running await", watch, Job{Phase: "P0", Kind: JobVerify, Command: "make qa", Commit: "c2"}, false, false, true},
		{"the same gate on a newer commit", gate, Job{Phase: "P0", Kind: JobVerify, Command: "make qa", Commit: "c2"}, false, true, false},
		{"the same wait again", watch, Job{Phase: "P0", Kind: JobAwait, Command: "gh run watch 7 --exit-status"}, false, true, false},
		{"another gate", gate, Job{Phase: "P0", Kind: JobVerify, Command: "make e2e", Commit: "c2"}, true, true, false},
		{"the same gate in the working tree", gate, Job{Phase: "P0", Kind: JobVerify, Command: "make qa", Commit: "c2", Here: true}, true, true, false},
		{"another wait", watch, Job{Phase: "P0", Kind: JobAwait, Command: "gh run watch 8 --exit-status"}, true, true, false},
		{"a gate recorded with no kind", Job{ID: "j1", Phase: "P0", Command: "make qa", Status: JobQueued}, Job{Phase: "P0", Kind: JobVerify, Command: "make qa"}, false, true, false},
		{"another gate once the first ended", Job{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "make qa", Status: JobFailed}, Job{Phase: "P0", Kind: JobVerify, Command: "make e2e"}, false, false, false},
		{"another phase's gate", Job{ID: "j1", Phase: "P1", Kind: JobVerify, Command: "make qa", Status: JobRunning}, Job{Phase: "P0", Kind: JobVerify, Command: "make e2e"}, false, false, false},
	} {
		st := State{Jobs: []Job{c.old}}
		if got := st.Clash(c.new); (got != nil) != c.clash || got != nil && got.ID != "j1" {
			t.Errorf("%s: clash %+v", c.name, got)
		}
		q, replaced, _ := QueueJob(&st, c.new, t0)
		if (replaced != nil) != c.replaced || replaced != nil && replaced.ID != "j1" {
			t.Errorf("%s: replaced %+v", c.name, replaced)
		}
		if old := st.Job("j1"); old.Finished() != (c.replaced || c.old.Finished()) {
			t.Errorf("%s: the old job %+v", c.name, *old)
		}
		if b := st.Beside(q); (b != nil) != c.beside || b != nil && b.ID != "j1" {
			t.Errorf("%s: beside %+v", c.name, b)
		}
		if c.beside && len(st.Running("P0")) != 2 {
			t.Errorf("%s: running %+v", c.name, st.Running("P0"))
		}
	}
}

// A phase stands on its latest job of each kind: a red wait on CI is not hidden by a gate run after it,
// and a job that a later one of its kind replaced stands no more.
func TestStanding(t *testing.T) {
	jobs := []Job{
		{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "make qa", Status: JobFailed},
		{ID: "j2", Phase: "P0", Kind: JobAwait, Command: "gh run watch 7", Status: JobFailed},
		{ID: "j3", Phase: "P0", Kind: JobVerify, Command: "make qa", Status: JobPassed},
		{ID: "j4", Phase: "P1", Kind: JobVerify, Command: "make test", Status: JobTimeout},
		{ID: "j5", Phase: "P2", Command: "make lint", Status: JobFailed}, // a gate recorded with no kind
		{ID: "j6", Phase: "P2", Kind: JobAwait, Command: "gh run watch 8", Status: JobRunning},
	}
	st := State{Jobs: jobs}
	for phase, want := range map[string][]string{"P0": {"j3", "j2"}, "P1": {"j4"}, "P2": {"j5", "j6"}, "P3": nil} {
		if got := ids(st.Standing(phase)); !slices.Equal(got, want) {
			t.Errorf("%s stands on %v, want %v", phase, got, want)
		}
	}
	if got := ids(st.Red()); !slices.Equal(got, []string{"j2", "j4", "j5"}) {
		t.Errorf("red: %v", got)
	}
	if got := st.Latest("P0"); got.ID != "j3" {
		t.Errorf("what ran last: %s", got.ID)
	}
}

// done leaves a phase verifying while either of its jobs runs, on the jobs that run then, and is refused
// while it stands on a red one. Finished despite a red job, it is not reopened by that job once the other
// passes.
func TestDoneStandsOnEachKind(t *testing.T) {
	p := graph("P0:", "P1:")
	t1 := t0.Add(time.Hour)
	verify := func(id, status string) Job {
		return Job{ID: id, Phase: "P0", Kind: JobVerify, Command: "make qa", Status: status, Notes: "qa set"}
	}
	await := func(id, status string) Job {
		return Job{ID: id, Phase: "P0", Kind: JobAwait, Command: "gh run watch 7", Status: status, Notes: "ci"}
	}
	for _, c := range []struct {
		name    string
		jobs    []Job
		despite bool
		red     string   // the job done is refused on
		on      []string // the jobs it verifies on; none: done
	}{
		{"both running", []Job{verify("j1", JobRunning), await("j2", JobRunning)}, false, "", []string{"j1", "j2"}},
		{"the gate passed, the wait runs", []Job{verify("j1", JobPassed), await("j2", JobRunning)}, false, "", []string{"j2"}},
		{"both passed", []Job{verify("j1", JobPassed), await("j2", JobPassed)}, false, "", nil},
		{"a red gate beside a running wait", []Job{verify("j1", JobFailed), await("j2", JobRunning)}, false, "j1", nil},
		{"a red wait before a gate that passed", []Job{await("j1", JobFailed), verify("j2", JobPassed)}, false, "j1", nil},
		{"a red wait, finished despite it", []Job{await("j1", JobFailed), verify("j2", JobRunning)}, true, "", []string{"j2"}},
		{"a wait that ended with no verdict", []Job{await("j1", JobInterrupted), verify("j2", JobRunning)}, false, "", []string{"j2"}},
	} {
		st := Attach(p, t0, Origin{})
		st.Jobs = c.jobs
		_, err := Done(&st, p, "P0", t0, DoneOpts{DespiteRed: c.despite})
		var re RedError
		if errors.As(err, &re) != (c.red != "") || c.red != "" && re.Job.ID != c.red {
			t.Errorf("%s: done %v", c.name, err)
			continue
		}
		if c.red != "" {
			continue
		}
		ps := st.Phases["P0"]
		if c.on == nil {
			if ps.Status != PhaseDone || ps.On != nil {
				t.Errorf("%s: P0 %+v", c.name, ps)
			}
			continue
		}
		if ps.Status != PhaseVerifying || !slices.Equal(ps.On, c.on) || !slices.Equal(ids(st.WaitsOn("P0")), c.on) || !st.HeldOn("P0") {
			t.Errorf("%s: P0 %+v", c.name, ps)
			continue
		}
		for i := range st.Jobs {
			if !st.Jobs[i].Finished() {
				st.Jobs[i].Status = JobPassed
			}
		}
		if got := Settle(&st, p, t1); len(got.Verdicts) != 1 || got.Verdicts[0].Park != "" || ps.Status != PhaseDone || ps.On != nil {
			t.Errorf("%s: once its jobs passed, settled %+v, P0 %+v", c.name, got, ps)
		}
	}
}

// Settle marks a phase verifying on a gate and a wait done once both passed, and reopens it, parked, on
// the first that fails or ends with no verdict, while the other runs on. A parked phase comes back once its
// jobs give it something to act on.
func TestSettleOnEachKind(t *testing.T) {
	p := graph("P0:", "P1:")
	t1 := t0.Add(time.Hour)
	for _, c := range []struct {
		name          string
		verify, await string // their statuses
		park          string // "" with phase done; "-" while it still verifies
		job           string // the job that decided
	}{
		{"both run", JobRunning, JobRunning, "-", ""},
		{"the gate passed, the wait runs", JobPassed, JobRunning, "-", ""},
		{"both passed", JobPassed, JobPassed, "", "j2"},
		{"the gate passed, the wait failed", JobPassed, JobFailed, ParkRed, "j2"},
		{"the gate failed, the wait runs", JobFailed, JobRunning, ParkRed, "j1"},
		{"the wait timed out, the gate runs", JobRunning, JobTimeout, ParkRed, "j2"},
		{"the gate was interrupted, the wait runs", JobInterrupted, JobRunning, ParkUnverified, "j1"},
		{"the gate had no verdict, the wait failed", JobLaneError, JobFailed, ParkRed, "j2"},
	} {
		st := Attach(p, t0, Origin{})
		st.Jobs = []Job{
			{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "make qa", Status: JobRunning, Notes: "qa"},
			{ID: "j2", Phase: "P0", Kind: JobAwait, Command: "gh run watch 7", Status: JobRunning, Notes: "ci"},
		}
		if _, err := Done(&st, p, "P0", t0, DoneOpts{}); err != nil {
			t.Fatal(err)
		}
		st.Jobs[0].Status, st.Jobs[1].Status = c.verify, c.await
		if Unsettled(st) != (c.park != "-") {
			t.Errorf("%s: unsettled %v", c.name, Unsettled(st))
		}
		got := Settle(&st, p, t1)
		ps := st.Phases["P0"]
		switch {
		case c.park == "-":
			if got.Changed() || ps.Status != PhaseVerifying {
				t.Errorf("%s: settled %+v, P0 %+v", c.name, got, ps)
			}
			continue
		case len(got.Verdicts) != 1 || got.Verdicts[0].Park != c.park || got.Verdicts[0].Job.ID != c.job || len(got.Verdicts[0].Jobs) != 2:
			t.Errorf("%s: settled %+v", c.name, got)
			continue
		case c.park == "":
			if ps.Status != PhaseDone || st.Job("j1").Delivered.IsZero() || st.Job("j2").Delivered.IsZero() {
				t.Errorf("%s: P0 %+v, jobs %+v", c.name, ps, st.Jobs)
			}
			continue
		}
		if ps.Status != PhaseParked || ps.Park.Reason != c.park || ps.Park.Job != c.job || !st.resolved("P0", ps.Park) {
			t.Errorf("%s: P0 %+v, park %+v", c.name, ps, ps.Park)
		}
		// Taken up again at once, though its other job may still run.
		Start(&st, t1, Origin{})
		if next, _ := Done(&st, p, "P1", t1, DoneOpts{}); next != "P0" {
			t.Errorf("%s: after P1, %q is taken up", c.name, next)
		}
	}
}

// A phase in hand waiting on a gate and a wait is set aside on both, and comes back once both passed, or
// at once when one does not; a switch is called off on the same terms.
func TestSwitchOnEachKind(t *testing.T) {
	p := graph("P0:", "P1:", "P2:")
	two := func() State {
		st := waitingOnJob()
		st.Jobs = append(st.Jobs, Job{ID: "j2", Phase: "P0", Kind: JobAwait, Command: "gh run watch 7", Status: JobRunning, Notes: "ci watch queued", Queued: t0.Add(time.Minute)})
		return st
	}
	st := two()
	if got := ids(st.CurrentJobs()); !slices.Equal(got, []string{"j1", "j2"}) {
		t.Fatalf("current jobs %v", got)
	}
	sw, ok := Switch(&st, p, t0.Add(10*time.Minute))
	ps := st.Phases["P0"]
	if !ok || sw.From != "P0" || sw.To != "P1" || sw.Job != "j2" || ps.Park.Job != "j2" || ps.Park.Notes != "ci watch queued" || !slices.Equal(ps.On, []string{"j1", "j2"}) {
		t.Fatalf("switched %+v %v, P0 %+v park %+v", sw, ok, ps, ps.Park)
	}
	for _, c := range []struct {
		name          string
		verify, await string
		back          bool
	}{
		{"both run", JobRunning, JobRunning, false},
		{"the gate passed", JobPassed, JobRunning, false},
		{"the wait passed", JobRunning, JobPassed, false},
		{"both passed", JobPassed, JobPassed, true},
		{"the gate failed", JobFailed, JobRunning, true},
		{"the wait was interrupted", JobRunning, JobInterrupted, true},
	} {
		x := st
		x.Phases = map[string]*PhaseState{}
		for id, ps := range st.Phases {
			cp := *ps
			x.Phases[id] = &cp
		}
		x.Jobs = slices.Clone(st.Jobs)
		x.Jobs[0].Status, x.Jobs[1].Status = c.verify, c.await
		if got := Returning(x, p) == "P0"; got != c.back {
			t.Errorf("%s: P0 returning %v", c.name, got)
		}
		if _, ok := Unswitch(&x); ok != c.back {
			t.Errorf("%s: unswitched %v", c.name, ok)
		}
		if c.back && (x.Current != "P0" || x.Phases["P0"].Status != PhaseActive || x.Phases["P0"].On != nil) {
			t.Errorf("%s: unswitched to %q, P0 %+v", c.name, x.Current, x.Phases["P0"])
		}
	}

	// A checkpoint sets the phase aside on both, too.
	st = two()
	st.Phases["P1"].Status, st.Phases["P1"].Park = PhaseParked, &Park{Reason: ParkRed, Since: t0, Notes: "p1"}
	if to := SwitchAtCheckpoint(&st, p, "where P0 stands", t0.Add(time.Minute)); to != "P1" || !slices.Equal(st.Phases["P0"].On, []string{"j1", "j2"}) || st.Phases["P0"].Park.Job != "j2" {
		t.Errorf("checkpoint switched to %q, P0 %+v", to, st.Phases["P0"])
	}
}

// A phase agent's sub-run reads the same: a phase verifying on a red wait beside a gate that passed is not
// satisfied, and the merge queue's verification again waits on that alone.
func TestASubRunStandsOnEachKind(t *testing.T) {
	sub := Attach(graph("P3:"), t0, Origin{})
	sub.Phases["P3"].Status, sub.Phases["P3"].On = PhaseVerifying, []string{"P3.j1", "P3.j2"}
	sub.Jobs = []Job{
		{ID: "P3.j1", Phase: "P3", Kind: JobVerify, Command: "make test", Commit: "c1", Status: JobPassed},
		{ID: "P3.j2", Phase: "P3", Kind: JobAwait, Command: "gh run watch 7", Status: JobRunning},
	}
	w := Worker{Phase: "P3", Status: WorkerHeld}
	if !Satisfied(State{}, sub, w) {
		t.Error("not satisfied while its wait runs")
	}
	sub.Jobs[1].Status = JobFailed
	if Satisfied(State{}, sub, w) {
		t.Error("satisfied on a red wait")
	}

	done := Attach(graph("P3:"), t0, Origin{})
	done.Mode, done.Current = ModeComplete, ""
	done.Phases["P3"].Status = PhaseDone
	done.Jobs = slices.Clone(sub.Jobs)
	done.Jobs[1].Status = JobPassed
	q, err := Reverify(&done, Job{Phase: "P3", Command: "make test", Commit: "c2"}, t0)
	if err != nil || !slices.Equal(done.Phases["P3"].On, []string{q.ID}) || !done.HeldOn("P3") {
		t.Fatalf("reverify %+v %v, P3 %+v", q, err, done.Phases["P3"])
	}
}
