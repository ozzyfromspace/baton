package loop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// runners is a fake Runners: runner pids start at 100, and a pid is alive unless dead says otherwise.
type runners struct {
	spawned []string
	fail    error
	dead    map[int]bool
	killed  []int
	sweeps  atomic.Int32
}

func (f *runners) Spawn(run, id string) (int, error) {
	if f.fail != nil {
		return 0, f.fail
	}
	f.spawned = append(f.spawned, run+"/"+id)
	return 99 + len(f.spawned), nil
}
func (f *runners) Alive(pid int) bool { return !f.dead[pid] }
func (f *runners) Kill(pgid int)      { f.killed = append(f.killed, pgid) }
func (f *runners) Sweep()             { f.sweeps.Add(1) }

// jobRig is the rig with fake runners and nothing for baton to type.
func jobRig(t *testing.T) (*rig, *runners) {
	r := newRig(t)
	f := &runners{dead: map[int]bool{}}
	r.loop.Runners = f
	r.set(func(st *state.State) { st.Run.Compaction = state.Compaction{} })
	return r, f
}

func (r *rig) job(id string) state.Job {
	r.t.Helper()
	j := r.state().Job(id)
	if j == nil {
		r.t.Fatalf("no job %s", id)
	}
	return *j
}

func (r *rig) events() string {
	b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
	return string(b)
}

func TestTheHostStartsQueuedJobs(t *testing.T) {
	r, f := jobRig(t)
	r.set(func(st *state.State) {
		state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test"}, r.now)
	})
	r.tick(time.Second)
	run := filepath.Base(r.loop.Store.Dir)
	if len(f.spawned) != 1 || f.spawned[0] != run+"/j1" {
		t.Fatalf("spawned %v", f.spawned)
	}
	if j := r.job("j1"); j.Status != state.JobQueued || j.PID != 100 || !j.Spawned.Equal(r.now) {
		t.Fatalf("the job once its runner started: %+v", j)
	}
	r.run(time.Minute)
	if len(f.spawned) != 1 {
		t.Errorf("a job's runner was started again: %v", f.spawned)
	}
	if f.sweeps.Load() != 1 {
		t.Errorf("the lanes were swept %d times", f.sweeps.Load())
	}

	// A paused run's jobs run too: they go on without the session. A session the human is leaving
	// starts none.
	r.set(func(st *state.State) {
		state.Pause(st)
		state.QueueJob(st, state.Job{Phase: "P0"}, r.now)
	})
	r.tick(time.Second)
	r.set(func(st *state.State) { st.Run.ExitAt = r.now; state.QueueJob(st, state.Job{Phase: "P2"}, r.now) })
	r.run(time.Minute)
	if len(f.spawned) != 2 || f.spawned[1] != run+"/j2" {
		t.Errorf("spawned %v", f.spawned)
	}
}

func TestAJobWhoseRunnerCannotStartIsInterrupted(t *testing.T) {
	r, f := jobRig(t)
	f.fail = errors.New("no /bin/sh")
	r.set(func(st *state.State) { state.QueueJob(st, state.Job{Phase: "P1"}, r.now) })
	r.tick(time.Second)
	if j := r.job("j1"); j.Status != state.JobInterrupted || !strings.Contains(j.Reason, "could not start its runner: no /bin/sh") || !j.Ended.Equal(r.now) {
		t.Fatalf("job: %+v", j)
	}
	if ev := r.events(); !strings.Contains(ev, `"kind":"job_finished"`) || !strings.Contains(ev, `"by":"host"`) {
		t.Errorf("events: %s", ev)
	}
}

func TestAJobWhoseRunnerDiedIsInterrupted(t *testing.T) {
	for _, c := range []struct {
		name   string
		job    func(now time.Time) state.Job
		dead   bool
		after  time.Duration
		reason string // "" for a job left alone
	}{
		{"running, its runner beating", running(0), false, time.Second, ""},
		{"running, its runner gone", running(0), true, time.Second, "its runner (pid 42) is gone"},
		{"running, its runner silent a while", running(-90 * time.Second), false, time.Second, ""},
		{"running, its runner silent too long", running(-DefaultTiming.JobStale), false, time.Second, "its runner (pid 42) has not been heard from for 2m1s"},
		{"spawned, its runner starting", spawned(-20 * time.Second), true, time.Second, ""},
		{"spawned, its runner still there", spawned(-time.Hour), false, time.Second, ""},
		{"spawned, its runner gone", spawned(-DefaultTiming.SpawnGrace), true, time.Second, "its runner (pid 42) ended before it started the job"},
		{"finished", func(now time.Time) state.Job { return state.Job{ID: "j1", Status: state.JobPassed, PID: 42} }, true, time.Second, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, f := jobRig(t)
			r.run(3 * time.Minute) // deadlines count from the host's start: let them run
			f.dead[42] = c.dead
			r.set(func(st *state.State) { st.Jobs = []state.Job{c.job(r.now)} })
			before := r.job("j1")
			r.tick(c.after)
			j := r.job("j1")
			if c.reason == "" {
				if j.Status != before.Status || len(f.killed) != 0 {
					t.Fatalf("job %+v, killed %v", j, f.killed)
				}
				return
			}
			if j.Status != state.JobInterrupted || j.Reason != c.reason || j.Exit != -1 || !j.Ended.Equal(r.now) {
				t.Fatalf("job: %+v", j)
			}
			if before.PGID != 0 && (len(f.killed) != 1 || f.killed[0] != before.PGID) {
				t.Errorf("killed %v, want its group %d", f.killed, before.PGID)
			}
			if ev := r.events(); !strings.Contains(ev, `"kind":"job_finished"`) || !strings.Contains(ev, c.reason) {
				t.Errorf("events: %s", ev)
			}
		})
	}
}

// A phase set aside while its job runs hears nothing until it comes back, so the human is told when the
// job ends, even when its runner died.
func TestTheEndOfAParkedPhasesJobIsAnnounced(t *testing.T) {
	r, f := jobRig(t)
	r.run(3 * time.Minute)
	f.dead[42] = true
	r.set(func(st *state.State) {
		j := running(0)(r.now)
		j.Command = "make test"
		st.Jobs = []state.Job{j}
		st.Phases["P0"].Status, st.Current, st.BoundaryOwed = state.PhaseActive, "P0", false
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: state.ParkJob, Job: "j1"}}
	})
	r.tick(time.Second)
	want := "baton: job j1 (`make test`, P1) was interrupted after 1h00m: its runner (pid 42) is gone → P1 comes back once P0 is done or set aside"
	if got := r.state().Run.Announce; len(got) != 1 || got[0] != want {
		t.Errorf("announced %q, want %q", got, want)
	}
}

func running(beat time.Duration) func(now time.Time) state.Job {
	return func(now time.Time) state.Job {
		return state.Job{ID: "j1", Phase: "P1", Status: state.JobRunning, PID: 42, PGID: 43, Lane: 1, Started: now.Add(-time.Hour), Heartbeat: now.Add(beat)}
	}
}

func spawned(ago time.Duration) func(now time.Time) state.Job {
	return func(now time.Time) state.Job {
		return state.Job{ID: "j1", Phase: "P1", Status: state.JobQueued, PID: 42, Spawned: now.Add(ago)}
	}
}

// A runner cannot beat while the machine sleeps, and neither can the host watch it: its silence counts
// from the wake.
func TestARunnerIsNotTakenForDeadAfterTheMachineSlept(t *testing.T) {
	r, _ := jobRig(t)
	r.tick(0)
	r.set(func(st *state.State) { st.Jobs = []state.Job{running(0)(r.now)} })
	r.tick(8 * time.Hour)
	if j := r.job("j1"); j.Status != state.JobRunning {
		t.Fatalf("after the sleep: %+v", j)
	}
	r.run(DefaultTiming.JobStale + 10*time.Second)
	if j := r.job("j1"); j.Status != state.JobInterrupted {
		t.Fatalf("a runner silent since the wake: %+v", j)
	}
}

// A job that ended just as the host looked is left as its runner recorded it.
func TestAJobThatEndedMeanwhileIsLeftAlone(t *testing.T) {
	r, f := jobRig(t)
	r.tick(0)
	j := running(0)(r.now)
	r.set(func(st *state.State) { st.Jobs = []state.Job{j} })
	r.set(func(st *state.State) { st.Jobs[0].Status = state.JobPassed })
	r.loop.lost(r.view, j, "its runner is gone")
	if got := r.job("j1"); got.Status != state.JobPassed || len(f.killed) != 0 {
		t.Fatalf("job %+v, killed %v", got, f.killed)
	}
}

// A job's runner outlives baton restarting itself, so a running job does not hold the restart.
func TestAJobDoesNotHoldARestart(t *testing.T) {
	r, _ := upgradeRig(t)
	r.loop.Runners = &runners{dead: map[int]bool{}}
	r.set(func(st *state.State) { st.Jobs = []state.Job{running(0)(r.now)} })
	r.tick(time.Second)
	if !r.restarted() {
		t.Fatal("a running job held the restart")
	}
}
