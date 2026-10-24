package state

import (
	"slices"
	"testing"
	"time"
)

// A whole repeat is a gate of a command that already ran whole in its phase, in this plan: with a verdict, or
// stopped after it showed a failure. A rerun of what failed, a narrowed one, an await, another command,
// another phase's run, and a run that checked nothing are not; nor is the first whole run itself, under way.
func TestWholeRepeat(t *testing.T) {
	gate := func(id, status string) Job {
		return Job{ID: id, Phase: "P3", Kind: JobVerify, Command: "make qa", Status: status}
	}
	stoppedAfterFailure := gate("j1", JobInterrupted)
	stoppedAfterFailure.Failing = &Failing{At: t0, Line: "--- FAIL: TestA"}
	for _, c := range []struct {
		name string
		jobs []Job
		j    Job // queued (with an id) or about to be (none)
		want string
	}{
		{"its first run", nil, gate("", ""), ""},
		{"after a red whole run", []Job{gate("j1", JobFailed)}, gate("", ""), "j1"},
		{"after a whole run that passed", []Job{gate("j1", JobPassed)}, gate("", ""), "j1"},
		{"after one that ran out of time", []Job{gate("j1", JobTimeout)}, gate("", ""), "j1"},
		{"after one stopped at its first failure", []Job{stoppedAfterFailure}, gate("", ""), "j1"},
		{"after one stopped before it showed anything", []Job{gate("j1", JobInterrupted)}, gate("", ""), ""},
		{"after one that could not run", []Job{gate("j1", JobLaneError)}, gate("", ""), ""},
		{"the latest of two whole runs", []Job{gate("j1", JobFailed), gate("j2", JobFailed)}, gate("", ""), "j2"},
		{"a whole repeat under way", []Job{gate("j1", JobFailed), gate("j2", JobRunning)}, gate("j2", JobRunning), "j1"},
		{"the first run under way", []Job{gate("j1", JobRunning)}, gate("j1", JobRunning), ""},
		{"a rerun of what failed", []Job{gate("j1", JobFailed)}, Job{Phase: "P3", Kind: JobVerify, Command: "make qa", Reruns: "j1"}, ""},
		{"a narrowed rerun", []Job{gate("j1", JobFailed)}, Job{Phase: "P3", Kind: JobVerify, Command: "go test -run '^TestA$' ./x", Reruns: "j1", Narrows: []string{"TestA"}}, ""},
		{"after only reruns", []Job{{ID: "j1", Phase: "P3", Kind: JobVerify, Command: "make qa", Reruns: "j0", Status: JobFailed}}, gate("", ""), ""},
		{"an await of the same command", []Job{gate("j1", JobFailed)}, Job{Phase: "P3", Kind: JobAwait, Command: "make qa"}, ""},
		{"another command", []Job{gate("j1", JobFailed)}, Job{Phase: "P3", Kind: JobVerify, Command: "make lint"}, ""},
		{"another phase's whole run", []Job{{ID: "j1", Phase: "P2", Kind: JobVerify, Command: "make qa", Status: JobFailed}}, gate("", ""), ""},
		{"a gate recorded with no kind", []Job{{ID: "j1", Phase: "P3", Command: "make qa", Status: JobFailed}}, gate("", ""), "j1"},
	} {
		st := State{Jobs: c.jobs}
		got := st.WholeRepeat(c.j)
		if (got == nil) != (c.want == "") || got != nil && got.ID != c.want {
			t.Errorf("%s: %+v, want %q", c.name, got, c.want)
		}
	}
}

// Wrapping up stops a QA phase's confirmation under way and settles the phase on the rerun that passed
// before it: Settle finds it done, with no confirmation queued again and an await it waits on kept. One owed
// while its rerun runs is dropped, and the phase is done once that rerun passes. A phase with neither is
// left alone.
func TestWrappingUpSettlesAConfirmationOnItsRerun(t *testing.T) {
	p := graph("P0:", "P1:")
	t1 := t0.Add(time.Hour)
	setup := func(rerun string, confirm *Confirm, on []string, more ...Job) State {
		st := Attach(p, t0, Origin{})
		st.Jobs = append([]Job{
			{ID: "j1", Phase: "P0", Kind: JobVerify, Command: "./qa.sh", Status: JobFailed, Exit: 1, QAPhase: true},
			{ID: "j2", Phase: "P0", Kind: JobVerify, Command: "./qa.sh", Reruns: "j1", Status: rerun, QAPhase: true},
		}, more...)
		st.JobSeq = len(st.Jobs)
		ps := st.Phases["P0"]
		ps.Status, ps.On, ps.Confirm = PhaseVerifying, on, confirm
		st.Current = "P1"
		st.Phases["P1"] = &PhaseState{Status: PhaseActive, StartedAt: t0}
		return st
	}
	conf := Job{ID: "j3", Phase: "P0", Kind: JobVerify, Command: "./qa.sh", Requeue: true, QAPhase: true, Status: JobRunning}
	ci := Job{ID: "j4", Phase: "P0", Kind: JobAwait, Command: "gh run watch", Status: JobPassed}

	t.Run("a confirmation under way", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			on     []string
			more   []Job
			wantOn []string
		}{
			{"verifying on it alone", []string{"j3"}, []Job{conf}, []string{"j2"}},
			{"beside an await", []string{"j3", "j4"}, []Job{conf, ci}, []string{"j2", "j4"}},
		} {
			st := setup(JobPassed, &Confirm{Of: "j1", Command: "./qa.sh", Job: "j3"}, c.on, c.more...)
			if _, _, ok := StopConfirm(&st, "P1", "wrapping up", t1); ok {
				t.Fatalf("%s: stopped a confirmation P1 never had", c.name)
			}
			stopped, rerun, ok := StopConfirm(&st, "P0", "wrapping up", t1)
			ps := st.Phases["P0"]
			if !ok || stopped.ID != "j3" || stopped.Status != JobRunning || rerun != "j2" || ps.Confirm != nil || !slices.Equal(ps.On, c.wantOn) {
				t.Fatalf("%s: stopped %+v (%v), rerun %q, P0 %+v", c.name, stopped, ok, rerun, ps)
			}
			if j := st.Job("j3"); j.Status != JobInterrupted || j.Reason != "wrapping up" {
				t.Errorf("%s: the confirmation %+v", c.name, j)
			}
			got := Settle(&st, p, t1)
			if ps.Status != PhaseDone || len(got.Confirms) != 0 || len(got.Verdicts) != 1 || got.Verdicts[0].Park != "" || len(st.Jobs) != len(c.more)+2 {
				t.Errorf("%s: settled %+v, P0 %+v", c.name, got, ps)
			}
			if _, _, ok := StopConfirm(&st, "P0", "wrapping up", t1); ok {
				t.Errorf("%s: stopped twice", c.name)
			}
		}
	})

	t.Run("a confirmation owed", func(t *testing.T) {
		st := setup(JobRunning, &Confirm{Of: "j1", Command: "./qa.sh", After: "j2"}, []string{"j2"})
		if _, ok := DropConfirm(&st, "P1"); ok {
			t.Fatal("dropped a confirmation P1 never owed")
		}
		c, ok := DropConfirm(&st, "P0")
		if ps := st.Phases["P0"]; !ok || c.After != "j2" || ps.Confirm != nil || !slices.Equal(ps.On, []string{"j2"}) {
			t.Fatalf("dropped %+v (%v), P0 %+v", c, ok, ps)
		}
		st.Job("j2").Status = JobPassed
		got := Settle(&st, p, t1)
		if ps := st.Phases["P0"]; ps.Status != PhaseDone || len(got.Confirms) != 0 || len(st.Jobs) != 2 {
			t.Errorf("settled %+v, P0 %+v, jobs %+v", got, ps, st.Jobs)
		}
	})

	t.Run("a confirmation queued and ended is not one to stop or drop", func(t *testing.T) {
		done := conf
		done.Status = JobFailed
		st := setup(JobPassed, &Confirm{Of: "j1", Command: "./qa.sh", Job: "j3"}, []string{"j3"}, done)
		if _, _, ok := StopConfirm(&st, "P0", "wrapping up", t1); ok {
			t.Error("stopped a confirmation that had ended")
		}
		if _, ok := DropConfirm(&st, "P0"); ok || st.Phases["P0"].Confirm == nil {
			t.Error("dropped a confirmation that had run")
		}
	})
}

// What wrapping up did to a phase is a decision the human made: recorded with its undo, numbered in its run,
// shown in words of its own, and counted toward no review, even while the run is paused.
func TestWrapNote(t *testing.T) {
	st := Attach(graph("P0:"), t0, Origin{})
	st.IDPrefix, st.Mode = "P15.", ModePaused
	AddNote(&st, "an earlier note", "", t0) // refused while paused: nothing recorded
	d := WrapNote(&st, "P15", "stopped P15's confirmation run", "reopen P15 and run `bin/qa-set`", t0)
	if d.ID != "P15.d1" || d.Kind != Note || d.Phase != "P15" || d.Undo != "reopen P15 and run `bin/qa-set`" || !d.Announced || !d.Reviewed ||
		len(st.Decisions) != 1 || st.Decisions[0] != d {
		t.Fatalf("recorded %+v; decisions %+v", d, st.Decisions)
	}
	if !d.WithoutHuman() || st.Unattended("P15") != 0 {
		t.Errorf("counted %d toward a review", st.Unattended("P15"))
	}
}

// A new plan starts without the wrap of the plan before, however it is attached, and so does a dropped run.
func TestANewPlanClearsTheWrap(t *testing.T) {
	p := graph("P0:", "P1:")
	wrapped := func() State {
		st := Attach(p, t0, Origin{})
		st.Wrap = &Wrap{At: t0, By: WrapByHuman}
		return st
	}
	for _, c := range []struct {
		name   string
		attach func(State) State
	}{
		{"Reattach", func(st State) State { return Reattach(st, p, t0, Origin{}) }},
		{"AttachAtBoundary", func(st State) State { return AttachAtBoundary(st, p, t0) }},
		{"Detach", func(st State) State { Detach(&st); return st }},
	} {
		if st := c.attach(wrapped()); st.Wrap != nil {
			t.Errorf("%s kept %+v", c.name, st.Wrap)
		}
	}
	// A revision of the plan is the same plan: it wraps up still.
	st := wrapped()
	Revise(&st, p, t0)
	if st.Wrap == nil {
		t.Error("a revision of the plan dropped its wrap")
	}
}

// The lead's run's wrap is read by its own run and by its phase agents' sub-runs, without a lock.
func TestLeadWrapIsTheLeadRunsForItsAgents(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	withPlan(t, lead, "Plan")
	w := newWorker(t, p, lead, "P1")
	if lead.LeadWrap() != nil || w.LeadWrap() != nil {
		t.Fatal("a wrap before any")
	}
	wrap := Wrap{At: t0, By: WrapByHuman}
	lead.Update(func(st *State) error { st.Wrap = &wrap; return nil })
	w.Update(func(st *State) error { st.Wrap = nil; return nil })
	for name, s := range map[string]*Store{"the lead": lead, "its agent": w} {
		if got := s.LeadWrap(); got == nil || !got.At.Equal(wrap.At) || got.By != wrap.By {
			t.Errorf("%s reads %+v", name, got)
		}
	}
}
