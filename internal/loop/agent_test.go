package loop

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's loop keeps its sub-run claimed by its worker's instance, as often as a session's host
// refreshes its own claim: every other baton process sees the worker at work, and leaves it alone.
func TestAnAgentsLoopKeepsItsSubRunClaimed(t *testing.T) {
	x := newFleet(t)
	beat := func() *state.Owner {
		st, err := x.work.Load()
		if err != nil {
			t.Fatal(err)
		}
		return st.Owner
	}
	x.tick(time.Second)
	first := x.now
	if o := beat(); o == nil || o.Instance != state.WorkerInstance("inst", "P1") || !o.Heartbeat.Equal(first) || o.PID != os.Getpid() {
		t.Fatalf("owner %+v", o)
	}
	x.tick(beatEvery - 2*time.Second)
	if o := beat(); !o.Heartbeat.Equal(first) {
		t.Errorf("refreshed before its time: %+v", o)
	}
	x.tick(2 * time.Second)
	if o := beat(); !o.Heartbeat.Equal(x.now) {
		t.Errorf("not refreshed: %+v", o)
	}
}

// A phase agent's loop settles its sub-run's jobs: a phase verifying on a gate that passed is done, and
// with it the worker's sub-run, whose work the merge queue then lands.
func TestAnAgentsLoopSettlesItsJobs(t *testing.T) {
	x := newFleet(t)
	pl, err := x.work.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	x.set(x.work, func(st *state.State) {
		state.Start(st, x.now, state.Origin{})
		q, _, _ := state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test", Commit: "c1"}, x.now)
		id = q.ID
		j := st.Job(id)
		j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
		if _, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{}); err != nil {
			t.Fatal(err)
		}
	})
	if st, _ := x.work.Load(); st.Phases["P1"].Status != state.PhaseVerifying {
		t.Fatalf("P1 %+v", st.Phases["P1"])
	}
	x.set(x.work, func(st *state.State) {
		j := st.Job(id)
		j.Status, j.Ended = state.JobPassed, x.now
	})
	x.tick(time.Second)
	st, _ := x.work.Load()
	if st.Phases["P1"].Status != state.PhaseDone || st.Mode != state.ModeComplete {
		t.Errorf("P1 %+v; the sub-run %s", st.Phases["P1"], st.Mode)
	}
	if !strings.Contains(x.events(x.work), `"by":"verification"`) || strings.Contains(x.events(x.lead), "phase_done") {
		t.Errorf("the sub-run's events:\n%s\nthe lead's:\n%s", x.events(x.work), x.events(x.lead))
	}
}
