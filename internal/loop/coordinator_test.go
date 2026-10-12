package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// fleet is a coordinator for run sess-1, whose lead works on P0 while a phase agent works on P1: the lead
// with a run, a terminal and a view of its own, the agent with its sub-run and its loop (Agent). Runners
// and lanes are fake.
type fleet struct {
	t          *testing.T
	now        time.Time
	c          *Coordinator
	agent      *Agent
	lead, work *state.Store
	leadIn     *typed
	f          *runners
}

func newFleet(t *testing.T) *fleet {
	x := &fleet{t: t, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), leadIn: &typed{}, f: &runners{dead: map[int]bool{}}}
	p, err := state.OpenProject(filepath.Join(t.TempDir(), ".baton"), func() time.Time { return x.now })
	if err != nil {
		t.Fatal(err)
	}
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a", After: []string{}}, {ID: "P1", Title: "b", After: []string{}}}}
	if x.lead, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"}); err != nil {
		t.Fatal(err)
	}
	x.lead.WritePlan(pl)
	x.lead.Update(func(st *state.State) error {
		*st = state.Attach(pl, x.now, state.Origin{})
		return state.Claim(st, "inst", 1, x.now)
	})
	if x.work, err = p.NewWorker(x.lead.Ref, "P1", "inst", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P1")
	x.work.WritePlan(only)
	x.work.Update(func(st *state.State) error { *st = state.AttachWorker(*st, "P1"); return nil })
	x.c = &Coordinator{Lead: &Loop{Notify: &bomb{}, Project: "demo", Timing: DefaultTiming, Runners: x.f}, Lanes: x.f}
	x.agent = x.c.newAgent("P1", x.work)
	return x
}

// tick advances the clock by d and ticks the lead, then the agent's loop, as the coordinator's step does.
func (x *fleet) tick(d time.Duration) {
	x.now = x.now.Add(d)
	x.c.Tick(host.View{Now: x.now, Store: x.lead, Owner: true, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}, x.leadIn)
	x.agent.Tick(x.now)
}

func (x *fleet) set(s *state.Store, fn func(*state.State)) {
	s.Update(func(st *state.State) error { fn(st); return nil })
}

func (x *fleet) events(s *state.Store) string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	return string(b)
}

// bomb is a notifier that panics on a notice of kind "boom".
type bomb struct{}

func (bomb) Notify(project, kind, detail string) error {
	if kind == "boom" {
		panic("boom")
	}
	return nil
}

// Each loop's jobs are started for its own run, and recorded there.
func TestEachSessionsJobsRunAgainstItsOwnStore(t *testing.T) {
	x := newFleet(t)
	x.set(x.lead, func(st *state.State) {
		state.QueueJob(st, state.Job{Phase: "P0", Kind: state.JobVerify, Command: "make test"}, x.now)
	})
	x.set(x.work, func(st *state.State) {
		state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test"}, x.now)
	})
	x.tick(time.Second)
	if want := []string{"sess-1 j1", "sess-1/P1 P1.j1"}; strings.Join(x.f.spawned, ",") != strings.Join(want, ",") {
		t.Fatalf("spawned %q, want %q", x.f.spawned, want)
	}
	for _, c := range []struct {
		s       *state.Store
		id, not string
	}{{x.lead, "j1", "P1.j1"}, {x.work, "P1.j1", "j1"}} {
		st, _ := c.s.Load()
		if j := st.Job(c.id); j == nil || j.PID == 0 || !j.Spawned.Equal(x.now) {
			t.Errorf("%s: job %s: %+v", c.s.Ref, c.id, j)
		}
		if st.Job(c.not) != nil {
			t.Errorf("%s holds job %s", c.s.Ref, c.not)
		}
	}
}

// A panic in one loop's tick is logged in its run, once a minute, and every other loop goes on ticking,
// as do the run's own duties.
func TestAPanicInOneSessionLeavesTheOthersTicking(t *testing.T) {
	for _, c := range []struct {
		name string
		lead bool // the lead panics; otherwise the worker does
	}{{"the lead panics", true}, {"a phase agent's loop panics", false}} {
		t.Run(c.name, func(t *testing.T) {
			x := newFleet(t)
			bad, good, in, spawned := x.work, x.lead, "P1", "sess-1 j1"
			if c.lead {
				bad, good, in, spawned = x.lead, x.work, "lead", "sess-1/P1 P1.j1"
			}
			x.set(bad, func(st *state.State) { st.Run.Notices = []state.Notice{{Kind: "boom", Text: "x", At: x.now}} })
			x.set(good, func(st *state.State) { state.QueueJob(st, state.Job{Phase: "P0", Command: "make test"}, x.now) })
			x.tick(time.Second)
			x.tick(time.Second)
			if len(x.f.spawned) != 1 || x.f.spawned[0] != spawned {
				t.Errorf("spawned %q, want %q", x.f.spawned, spawned)
			}
			if !eventually(func() bool { return x.f.sweeps.Load() == 1 }) {
				t.Errorf("the lanes were swept %d times", x.f.sweeps.Load())
			}
			if n := strings.Count(x.events(bad), `"kind":"controller_panic"`); n != 1 || !strings.Contains(x.events(bad), `"error":"boom","in":"`+in+`"`) {
				t.Errorf("%d panics logged in %s:\n%s", n, bad.Ref, x.events(bad))
			}
			if strings.Contains(x.events(good), "controller_panic") {
				t.Errorf("a panic logged in %s:\n%s", good.Ref, x.events(good))
			}
			x.tick(time.Minute)
			if n := strings.Count(x.events(bad), `"kind":"controller_panic"`); n != 2 {
				t.Errorf("a minute later, %d panics logged", n)
			}
		})
	}
}

// The lanes are the whole run's: a worker's sub-run completing leaves them up, and the run's plan
// completing brings them down, once.
func TestLanesComeDownOnceTheRunsPlanIsComplete(t *testing.T) {
	x := newFleet(t)
	x.f.up = []int{1}
	x.set(x.work, func(st *state.State) { st.Mode = state.ModeComplete })
	for range 5 {
		x.tick(time.Second)
	}
	if x.f.downs.Load() != 0 {
		t.Fatalf("a worker's sub-run completing brought the lanes down %d times", x.f.downs.Load())
	}
	x.set(x.lead, func(st *state.State) { st.Mode = state.ModeComplete })
	x.tick(time.Second)
	if !eventually(func() bool { return strings.Contains(x.events(x.lead), `"kind":"lanes_down"`) }) {
		t.Fatalf("the run's plan is complete, and the lanes are still up: %d downs", x.f.downs.Load())
	}
	for range 5 {
		x.tick(time.Second)
	}
	if x.f.downs.Load() != 1 || strings.Contains(x.events(x.work), "lanes_down") {
		t.Errorf("brought down %d times; the worker's events:\n%s", x.f.downs.Load(), x.events(x.work))
	}
}
