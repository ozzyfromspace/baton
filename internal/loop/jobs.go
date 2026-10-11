package loop

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Runners starts and watches the runners of a run's jobs. Each job runs in a `baton lane-run` of its own,
// detached, so it outlives the session's claude and a baton that restarts itself (package lane). Tests
// fake it.
type Runners interface {
	// Spawn starts the runner of job id of run, and returns its pid.
	Spawn(run, id string) (pid int, err error)
	// Alive reports whether process pid is running.
	Alive(pid int) bool
	// Kill kills process group pgid: whatever a runner that died left running.
	Kill(pgid int)
	// Sweep removes the project's lanes that no job holds and no live host brought up.
	Sweep()
}

// jobs runs the run's jobs: it starts a runner for each job queued, and ends a job whose runner died as
// interrupted, since nothing else ever would. The runners do the rest, and record how each job ended. The
// first time, it sweeps up the lanes hosts that are gone left behind.
func (l *Loop) jobs(v host.View, st state.State) {
	if l.Runners == nil {
		return
	}
	if !l.swept {
		l.swept = true
		go l.Runners.Sweep()
	}
	for _, j := range st.Jobs {
		switch {
		case j.Status == state.JobQueued && j.Spawned.IsZero():
			l.spawn(v, j)
		case j.Status == state.JobQueued:
			if v.Now.Sub(l.since(j.Spawned)) > l.Timing.SpawnGrace && !l.Runners.Alive(j.PID) {
				l.lost(v, j, fmt.Sprintf("its runner (pid %d) ended before it started the job", j.PID))
			}
		case j.Status != state.JobRunning:
		case !l.Runners.Alive(j.PID):
			l.lost(v, j, fmt.Sprintf("its runner (pid %d) is gone", j.PID))
		default:
			if quiet := v.Now.Sub(l.since(latest(j.Heartbeat, j.Started))); quiet > l.Timing.JobStale {
				l.lost(v, j, fmt.Sprintf("its runner (pid %d) has not been heard from for %s", j.PID, quiet.Round(time.Second)))
			}
		}
	}
}

// spawn starts a queued job's runner, which takes the job from there.
func (l *Loop) spawn(v host.View, j state.Job) {
	pid, err := l.Runners.Spawn(filepath.Base(l.Store.Dir), j.ID)
	if err != nil {
		l.lost(v, j, fmt.Sprintf("baton could not start its runner: %v", err))
		return
	}
	l.logf("loop: started the runner of job %s (pid %d)", j.ID, pid)
	l.update(func(st *state.State) {
		if x := st.Job(j.ID); x != nil {
			x.Spawned = v.Now
			if x.PID == 0 {
				x.PID = pid
			}
		}
	})
}

// lost ends a job its runner will never finish (unless it ended meanwhile), as interrupted, and kills
// whatever its command left running.
func (l *Loop) lost(v host.View, j state.Job, reason string) {
	var ended *state.Job
	l.update(func(st *state.State) {
		x := st.Job(j.ID)
		if x == nil || x.Status != j.Status || x.PID != j.PID {
			return
		}
		x.Status, x.Exit, x.Reason, x.Ended = state.JobInterrupted, -1, reason, v.Now
		e := *x
		ended = &e
	})
	if ended == nil {
		return
	}
	if ended.PGID != 0 {
		l.Runners.Kill(ended.PGID)
	}
	l.logf("loop: job %s interrupted: %s", j.ID, reason)
	var seconds int
	if !j.Started.IsZero() {
		seconds = int(v.Now.Sub(j.Started).Seconds())
	}
	l.Store.Event("job_finished", map[string]any{"job": j.ID, "phase": j.Phase, "status": state.JobInterrupted, "exit": -1,
		"lane": ended.Lane, "reason": reason, "seconds": seconds, "by": "host"})
}
