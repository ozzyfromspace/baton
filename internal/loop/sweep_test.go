package loop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// abandoned leaves the run sess-1 as a host that ended leaves it: its workers on P1 and P2 suspended,
// P1's with a commit that never landed, and no host. It returns when the host ended.
func (x *crew) abandoned() time.Time {
	x.t.Helper()
	x.tick(time.Second)
	x.tick(time.Second)
	x.started("P1")
	x.started("P2")
	x.tick(time.Second)
	if len(x.sessions) != 2 {
		x.t.Fatalf("%d workers started, want 2", len(x.sessions))
	}
	for _, f := range x.sessions {
		f.exit(0)
	}
	x.c.Ended("the baton session ended")
	for _, w := range x.state().Workers {
		x.proj.Unbind(w.Instance)
	}
	x.proj.Unbind("inst")
	p1 := x.worker("P1")
	gittest.Write(x.t, p1.Worktree, "one.txt", "committed\n")
	gittest.Commit(x.t, p1.Worktree, "P1's work")
	return x.clock()
}

// quiet makes at the last time the run sess-1 changed, as its state file's time says: state.Abandoned
// counts it as a sign of life, and the file's time is the machine's, not the test's clock.
func (x *crew) quiet(at time.Time) {
	if err := os.Chtimes(filepath.Join(x.lead.Dir, "state.json"), at, at); err != nil {
		x.t.Fatal(err)
	}
}

// newHost is a host (instance) for a run of its own in x's project, the session sess-2's, with workers
// off and no plan yet. The pid of sess-1's host on record, if any, is a running baton when it is pid.
func (x *crew) newHost(instance string, pid int) (*Coordinator, *state.Store) {
	s, err := x.proj.Bind(state.Binding{Session: "sess-2", Instance: instance})
	if err != nil {
		x.t.Fatal(err)
	}
	c := &Coordinator{Lead: &Loop{Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}, Project: x.proj, Home: x.home,
		isBaton: func(p int) bool { return p == pid }}
	x.t.Cleanup(c.snapshotsTaken)
	return c, s
}

// tickHost advances the clock by a second and ticks host c of run s, and waits for its work on workers.
func (x *crew) tickHost(c *Coordinator, s *state.Store) {
	x.advance(time.Second)
	c.Tick(host.View{Now: x.now, Store: s, Owner: true, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}, &typed{})
	c.fleetWG.Wait()
}

// attachTo attaches a plan to run s, as /baton plan does once it is approved.
func (x *crew) attachTo(s *state.Store, title string) {
	pl := plan.Plan{Version: 1, Title: title, File: "/plans/" + title + ".md", Phases: []plan.Phase{{ID: "P0", Title: "zero", After: []string{}}}}
	s.WritePlan(pl)
	s.Update(func(st *state.State) error {
		*st = state.Reattach(*st, pl, x.now, state.Origin{})
		st.RunBranch = "main"
		return nil
	})
}

func readEvents(s *state.Store) string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	return string(b)
}

// A plan attached in one session retires the workers that another run, which no session will come back
// to, left behind: their work is saved and their worktrees removed, a branch holding commits that never
// landed is kept by its name, and their phases go back to their run for its next host. A run whose host
// still runs, ended a minute ago, or slept with the machine is left alone.
func TestANewRunRetiresTheWorkersOthersLeft(t *testing.T) {
	for _, c := range []struct {
		name    string
		after   time.Duration // from when sess-1's host ended to when sess-2's starts
		owner   func(x *crew, ended time.Time) *state.Owner
		fleet   bool          // sess-2's host runs workers
		quiet   time.Duration // how long sess-2's host waits for a quiet run (0: state.AbandonAfter)
		retired bool
	}{
		{"its host ended", 3 * time.Minute, nil, false, 0, true},
		{"its host ended, swept by a host with workers", 3 * time.Minute, nil, true, 0, true},
		{"its host was killed", 3 * time.Minute, func(x *crew, ended time.Time) *state.Owner {
			return &state.Owner{Instance: "inst", PID: 4243, Heartbeat: ended}
		}, false, 0, true},
		{"its host ended a minute ago", time.Minute, nil, false, 0, false},
		{"its host ended a minute ago, and a test waits 30s", time.Minute, nil, false, 30 * time.Second, true},
		{"another host drives it now", 3 * time.Minute, func(x *crew, ended time.Time) *state.Owner {
			return &state.Owner{Instance: "inst3", PID: 4243, Heartbeat: ended.Add(3 * time.Minute)}
		}, false, 0, false},
		{"its host's baton still runs (the machine slept)", 3 * time.Minute, func(x *crew, ended time.Time) *state.Owner {
			return &state.Owner{Instance: "inst", PID: 4242, Heartbeat: ended}
		}, false, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 2)
			ended := x.abandoned()
			before := x.state()
			if c.owner != nil {
				o := c.owner(x, ended)
				x.lead.Update(func(st *state.State) error { st.Owner = o; return nil })
			}
			x.quiet(ended)
			x.advance(c.after)
			c2, s2 := x.newHost("inst2", 4242)
			c2.AbandonAfter = c.quiet
			if c.fleet {
				c2.Fleet = &Fleet{Max: 1, Go: func(fn func()) { fn() }, Machine: func() machine.Reading { return roomy },
					Spawn: func(host.Spec) (Hidden, error) { return nil, errors.New("no sessions here") }}
			}
			x.tickHost(c2, s2) // the host starts with no plan
			if st := x.state(); st.Workers[0].Status != state.WorkerSuspended || st.Workers[1].Status != state.WorkerSuspended {
				t.Fatalf("swept before a plan was attached: %+v", st.Workers)
			}
			x.attachTo(s2, "Next")
			x.tickHost(c2, s2)
			x.tickHost(c2, s2)

			st, _ := s2.Load()
			old := x.state()
			p1, p2 := x.worker("P1"), x.worker("P2")
			if !c.retired {
				if p1.Status != state.WorkerSuspended || p2.Status != state.WorkerSuspended || !isDir(p1.Worktree) || !isDir(p2.Worktree) {
					t.Errorf("retired: %+v", old.Workers)
				}
				if ev := readEvents(s2); strings.Contains(ev, "workers_retired") {
					t.Errorf("events:\n%s", ev)
				}
				return
			}
			for _, w := range []state.Worker{p1, p2} {
				if w.Status != state.WorkerFailed || w.Why != whyNewRun || w.Retirer != "inst2" || isDir(w.Worktree) {
					t.Errorf("worker %+v; worktree there %v", w, isDir(w.Worktree))
				}
				if ps := old.Phases[w.Phase]; ps.Status != state.PhasePending {
					t.Errorf("%s %+v", w.Phase, ps)
				}
			}
			// The plan sess-1 worked for still stands: P1's next worker carries on from its commits.
			if p1.Branch != "baton/sess-1/P1" || !x.branch(p1.Branch) || p1.Unlanded != 1 || x.branch(p2.Branch) {
				t.Errorf("P1's branch %q (there %v, %d unlanded); P2's there %v", p1.Branch, x.branch(p1.Branch), p1.Unlanded, x.branch(p2.Branch))
			}
			if old.Current != before.Current || old.Mode != state.ModeRunning || old.Generation != before.Generation {
				t.Errorf("sess-1 changed: %s, %s, generation %d", old.Current, old.Mode, old.Generation)
			}
			if ev := x.events(); strings.Count(ev, `"kind":"worker_ended","phase"`) < 2 || strings.Count(ev, `"by":"inst2"`) != 2 {
				t.Errorf("sess-1's events:\n%s", ev)
			}
			ev := readEvents(s2)
			if strings.Count(ev, `"kind":"workers_retired"`) != 1 || !strings.Contains(ev, `"run":"sess-1"`) || !strings.Contains(ev, `"phases":["P1","P2"]`) ||
				!strings.Contains(ev, `"kept":{"baton/sess-1/P1":1}`) {
				t.Errorf("sess-2's events:\n%s", ev)
			}
			want := decide.WorkersRetired([]decide.Left{{Phase: "P1", Branch: "baton/sess-1/P1", Unlanded: 1}, {Phase: "P2"}})
			if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, want) {
				t.Errorf("announced %q, want %q", ann, want)
			}
			if want != "baton: ended 2 workers left by an earlier run (P1, P2); its branch baton/sess-1/P1 keeps 1 commit that never landed" {
				t.Errorf("the line: %q", want)
			}
		})
	}
}

// The plan a host finds attached as it starts counts as seen: a host that starts (or restarts) sweeps
// nothing. The next plan attached does.
func TestAHostThatStartsSweepsNothing(t *testing.T) {
	x := newCrew(t, 2)
	ended := x.abandoned()
	x.quiet(ended)
	x.advance(3 * time.Minute)
	c2, s2 := x.newHost("inst2", 0)
	x.attachTo(s2, "Next")
	x.tickHost(c2, s2)
	x.tickHost(c2, s2)
	if st := x.state(); st.Workers[0].Status != state.WorkerSuspended || st.Workers[1].Status != state.WorkerSuspended || strings.Contains(readEvents(s2), "workers_retired") {
		t.Fatalf("swept as the host started: %+v", st.Workers)
	}
	x.attachTo(s2, "Another")
	x.tickHost(c2, s2)
	if x.worker("P1").Status != state.WorkerFailed || !strings.Contains(readEvents(s2), "workers_retired") {
		t.Errorf("not swept once the next plan was attached: %+v", x.state().Workers)
	}
}

// With no other run's workers on record, a plan attached changes nothing anywhere.
func TestASweepWithNothingLeft(t *testing.T) {
	x := newCrew(t, 2) // sess-1 has a plan, and no worker ever started
	statePath := filepath.Join(x.lead.Dir, "state.json")
	b, _ := os.ReadFile(statePath)
	c2, s2 := x.newHost("inst2", 0)
	x.advance(3 * time.Minute)
	x.tickHost(c2, s2)
	x.attachTo(s2, "Next")
	x.tickHost(c2, s2)
	if after, _ := os.ReadFile(statePath); string(after) != string(b) {
		t.Errorf("sess-1 changed:\n%s\nwas\n%s", after, b)
	}
	if ev := readEvents(s2); strings.Contains(ev, "workers_retired") || strings.Contains(ev, "worker_ended") {
		t.Errorf("events:\n%s", ev)
	}
}

// ps names baton by its path on macOS and by its name on Linux.
func TestBatonName(t *testing.T) {
	for comm, want := range map[string]bool{
		"/Users/h/.baton/bin/v0.5.0/baton\n": true, "baton": true, "baton.exe": true,
		"claude": false, "/bin/zsh": false, "batons": false, "": false,
	} {
		if got := batonName(comm); got != want {
			t.Errorf("%q: %v, want %v", comm, got, want)
		}
	}
	// Windows has no ps, so there a process that is alive counts as a baton.
	if batonRunning(os.Getpid()) != (runtime.GOOS == "windows") {
		t.Error("the test binary is taken for a baton")
	}
	if batonRunning(0) || batonRunning(-1) {
		t.Error("no process is taken for a baton")
	}
}
