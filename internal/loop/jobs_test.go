package loop

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// runners is a fake Runners, and the project's Lanes: runner pids start at 100, and a pid is alive unless
// dead says otherwise.
type runners struct {
	spawned []string
	fail    error
	dead    map[int]bool
	killed  []int
	sweeps  atomic.Int32
	up      []int // the lanes this host brought up, which Down brings down
	downs   atomic.Int32
	cpu     map[int]procs.Usage // what Usage reads of each process group: none, unless the test says
	cpuErr  error
}

func (f *runners) Usage(pgid int) (procs.Usage, error) {
	if f.cpuErr != nil {
		return nil, f.cpuErr
	}
	return maps.Clone(f.cpu[pgid]), nil
}

func (f *runners) Spawn(ref, id string) (int, error) {
	if f.fail != nil {
		return 0, f.fail
	}
	f.spawned = append(f.spawned, ref+" "+id)
	return 99 + len(f.spawned), nil
}
func (f *runners) Alive(pid int) bool { return !f.dead[pid] }
func (f *runners) Kill(pgid int)      { f.killed = append(f.killed, pgid) }
func (f *runners) Sweep()             { f.sweeps.Add(1) }
func (f *runners) Down() []int        { f.downs.Add(1); return f.up }

// eventually waits a moment for what a goroutine of the loop's does.
func eventually(cond func() bool) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// jobRig is the rig with fake runners and nothing for baton to type.
func jobRig(t *testing.T) (*rig, *runners) {
	r := newRig(t)
	f := &runners{dead: map[int]bool{}}
	r.loop.Runners, r.coord.Lanes = f, f
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
	if len(f.spawned) != 1 || f.spawned[0] != run+" j1" {
		t.Fatalf("spawned %v", f.spawned)
	}
	if j := r.job("j1"); j.Status != state.JobQueued || j.PID != 100 || !j.Spawned.Equal(r.now) {
		t.Fatalf("the job once its runner started: %+v", j)
	}
	r.run(time.Minute)
	if len(f.spawned) != 1 {
		t.Errorf("a job's runner was started again: %v", f.spawned)
	}
	if !eventually(func() bool { return f.sweeps.Load() == 1 }) { // the coordinator sweeps off the tick
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
	if len(f.spawned) != 2 || f.spawned[1] != run+" j2" {
		t.Errorf("spawned %v", f.spawned)
	}
}

// The lanes come down once the plan is complete, and again for a plan attached later, once that one is.
func TestAFinishedPlansLanesComeDown(t *testing.T) {
	r, f := jobRig(t)
	f.up = []int{1}
	r.run(time.Minute)
	r.set(func(st *state.State) { st.Mode = state.ModePaused })
	r.run(time.Minute)
	if f.downs.Load() != 0 {
		t.Fatalf("a plan not complete brought its lanes down %d times", f.downs.Load())
	}
	// Each time, the test waits for the goroutine's event before its clock moves on.
	downs := func(n int) bool {
		return eventually(func() bool { return strings.Count(r.events(), `"kind":"lanes_down"`) == n })
	}
	r.set(func(st *state.State) { st.Mode = state.ModeComplete })
	r.tick(time.Second)
	if !downs(1) {
		t.Fatalf("brought down %d times; events: %s", f.downs.Load(), r.events())
	}
	if ev := r.events(); !strings.Contains(ev, `"lanes":[1]`) || !strings.Contains(ev, `"why":"plan complete"`) {
		t.Errorf("events: %s", ev)
	}
	r.run(time.Minute)
	if f.downs.Load() != 1 {
		t.Errorf("brought down %d times", f.downs.Load())
	}
	r.set(func(st *state.State) { st.Mode = state.ModeRunning })
	r.tick(time.Second)
	r.set(func(st *state.State) { st.Mode = state.ModeComplete })
	r.tick(time.Second)
	if !downs(2) {
		t.Errorf("the next plan's lanes: brought down %d times in all", f.downs.Load())
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

// gateRig is a rig whose phase P1 runs a gate (job j1, its command's group 43) with a log of its own, its
// runner beating, as the host has been up a while.
func gateRig(t *testing.T, edit func(*state.Job)) (*rig, *runners, string) {
	r, f := jobRig(t)
	r.run(3 * time.Minute)
	log := filepath.Join(t.TempDir(), "j1.log")
	os.WriteFile(log, []byte("[baton] job j1: make test\n"), 0o644)
	f.cpu = map[int]procs.Usage{43: {43: 100, 44: 7}}
	r.set(func(st *state.State) {
		j := running(0)(r.now)
		j.Kind, j.Command, j.Log = state.JobVerify, "make test", log
		if edit != nil {
			edit(&j)
		}
		st.Jobs = []state.Job{j}
	})
	return r, f, log
}

// live runs the gate rig for d in 10-second ticks, its runner beating, and each minute does what moves.
func live(r *rig, d time.Duration, each func(minute int)) {
	for at := time.Duration(0); at < d; at += 10 * time.Second {
		if at%time.Minute == 0 && each != nil {
			each(int(at / time.Minute))
		}
		r.set(func(st *state.State) {
			if j := st.Job("j1"); j != nil && j.Status == state.JobRunning {
				j.Heartbeat = r.now
			}
		})
		r.tick(10 * time.Second)
	}
}

// A gate whose log has not grown, and whose process tree has used no CPU, for HungAfter is stopped as
// hung, and counts as unverified; one that shows a sign of life of either kind, or whose processes
// cannot be read, never is. Nor is an await, or a gate waiting for a lane.
func TestAGateThatHangsIsStopped(t *testing.T) {
	idle := func(*runners, int) {}
	for _, c := range []struct {
		name string
		job  func(*state.Job)
		cpu  func(f *runners, minute int) // what the processes do each minute
		log  bool                         // the log grows each minute
		hung bool
	}{
		{"a quiet log, idle processes", nil, idle, false, true},
		{"a quiet log, busy processes", nil, func(f *runners, _ int) { f.cpu[43][44] += 3 }, false, false},
		{"a quiet log, a process started", nil, func(f *runners, m int) { f.cpu[43][100+m] = 0 }, false, false},
		{"a quiet log, no CPU reading", nil, func(f *runners, _ int) { f.cpuErr = procs.ErrUnsupported }, false, false},
		{"a quiet log, its processes gone", nil, func(f *runners, _ int) { delete(f.cpu, 43) }, false, false},
		{"a growing log, idle processes", nil, idle, true, false},
		{"busy once, a while ago", nil, func(f *runners, m int) {
			if m == 10 {
				f.cpu[43][43]++
			}
		}, false, false},
		{"an await", func(j *state.Job) { j.Kind = state.JobAwait }, idle, false, false},
		{"a gate waiting for a lane", func(j *state.Job) { j.LaneWait, j.PGID = j.Started, 0 }, idle, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, f, log := gateRig(t, c.job)
			live(r, DefaultTiming.HungAfter+5*time.Minute, func(m int) {
				c.cpu(f, m)
				if c.log {
					w, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
					w.WriteString("ok\n")
					w.Close()
				}
			})
			j := r.job("j1")
			if !c.hung {
				if j.Status != state.JobRunning || strings.Contains(r.events(), "job_hung") {
					t.Fatalf("stopped: %+v\n%s", j, r.events())
				}
				return
			}
			if j.Status != state.JobInterrupted || j.Red() || j.Exit != -1 || !decide.Hung(j) || !j.Delivered.IsZero() {
				t.Fatalf("job: %+v", j)
			}
			if want := decide.HungReason(DefaultTiming.HungAfter); j.Reason != want {
				t.Errorf("reason %q, want %q", j.Reason, want)
			}
			if len(f.killed) != 0 {
				t.Errorf("killed %v: its runner, which beats, stops its command", f.killed)
			}
			if !strings.Contains(r.events(), `"kind":"job_hung"`) || !strings.Contains(r.events(), `"quiet":"20m0s"`) {
				t.Errorf("events:\n%s", r.events())
			}
			if a := strings.Join(r.state().Run.Announce, "\n"); !strings.Contains(a, "baton: job j1 (`make test`, P1) looks hung") {
				t.Errorf("announced %q", a)
			}
		})
	}
}

// The count of a hung gate runs from the first look that saw it still, and only while the host looks:
// across the machine's sleep it starts afresh.
func TestAHungGateIsCountedWhileTheHostLooks(t *testing.T) {
	r, _, _ := gateRig(t, nil)
	live(r, DefaultTiming.HungAfter-time.Minute, nil)
	if j := r.job("j1"); j.Status != state.JobRunning {
		t.Fatalf("stopped before %s: %+v", DefaultTiming.HungAfter, j)
	}
	r.set(func(st *state.State) { st.Jobs[0].Heartbeat = r.now.Add(time.Hour) })
	r.tick(time.Hour) // the machine slept
	live(r, DefaultTiming.HungAfter-time.Minute, nil)
	if j := r.job("j1"); j.Status != state.JobRunning {
		t.Fatalf("its sleep counted: %+v", j)
	}
	live(r, 2*time.Minute, nil)
	if j := r.job("j1"); !decide.Hung(j) {
		t.Fatalf("not stopped: %+v", j)
	}
}
