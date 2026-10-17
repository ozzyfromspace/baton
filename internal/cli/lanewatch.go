package cli

import (
	"io"
	"sync"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// stageSettle is how long the runner waits, at a stage's end marker, for the stage's own record in the
// job's stages file (`baton stage` appends it as it prints the marker) before it records the stage from its
// markers alone; tests shorten it.
var stageSettle = 2 * time.Second

// stagePoll is how often it looks in the stages file meanwhile.
const stagePoll = 25 * time.Millisecond

// A jobWatch records what a job's output says as it runs (lane.Watcher): each stage as it ends, in the
// job's record and, for a stage nothing else recorded, its stages file; and the job's first failure
// (Job.Failing). Each is logged. It writes the run's state only for these, never for a line, and only
// while the job is the runner's own and running. Its work is done in order on a goroutine of its own, so
// the gate's output never waits on the state.
type jobWatch struct {
	s      *state.Store
	job    state.Job
	stages string // the job's stages file

	mu     sync.Mutex
	work   chan func()
	closed bool
	done   chan struct{}
	seen   map[string]int // the stages ended so far, by name, as their markers came
}

// watchJob starts watching job's output, which the Watcher it returns passes on to log.
func watchJob(s *state.Store, job state.Job, log io.Writer, now func() time.Time) (*lane.Watcher, *jobWatch) {
	jw := &jobWatch{s: s, job: job, stages: s.JobStages(job.ID), work: make(chan func(), 64), done: make(chan struct{}), seen: map[string]int{}}
	go func() {
		defer close(jw.done)
		for f := range jw.work {
			f()
		}
	}()
	w := &lane.Watcher{Out: log, Now: now, Commit: job.Commit,
		Ended: func(st lane.Stage) {
			jw.seen[st.Name]++
			k := jw.seen[st.Name]
			jw.queue(func() { jw.ended(st, k) })
		},
		Failed: func(line, stage string) {
			at := now()
			jw.queue(func() { jw.failing(at, line, stage) })
		},
	}
	return w, jw
}

// queue adds f to the work, unless the job has ended.
func (jw *jobWatch) queue(f func()) {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	if !jw.closed {
		jw.work <- f
	}
}

// ended records stage st, the k-th of its name to end: as the stages file has it, once `baton stage` has
// written it there, or else as its markers told, written there now.
func (jw *jobWatch) ended(st lane.Stage, k int) {
	rec, recorded := jw.record(st.Name, k)
	if !recorded {
		rec = st
	}
	if !jw.update(func(j *state.Job) bool { j.Stages = append(j.Stages, rec); return true }) {
		return
	}
	if !recorded {
		lane.AppendStage(jw.stages, rec)
	}
	jw.s.Event("stage_finished", map[string]any{"job": jw.job.ID, "phase": jw.job.Phase, "stage": rec.Name, "status": rec.Status,
		"seconds": int(rec.Took().Round(time.Second).Seconds()), "failures": len(rec.Failures)})
}

// record looks in the stages file, for a moment (stageSettle), for the k-th stage named name.
func (jw *jobWatch) record(name string, k int) (lane.Stage, bool) {
	deadline := time.Now().Add(stageSettle)
	for {
		stages, _ := lane.ReadStages(jw.stages)
		n := 0
		for _, st := range stages {
			if st.Name == name {
				if n++; n == k {
					return st, true
				}
			}
		}
		if !time.Now().Before(deadline) {
			return lane.Stage{}, false
		}
		time.Sleep(stagePoll)
	}
}

// failing records the job's first failure, seen at at in its output.
func (jw *jobWatch) failing(at time.Time, line, stage string) {
	if !jw.update(func(j *state.Job) bool {
		if j.Failing != nil {
			return false
		}
		j.Failing = &state.Failing{At: at, Line: line, Stage: stage}
		return true
	}) {
		return
	}
	jw.s.Event("job_failing", map[string]any{"job": jw.job.ID, "phase": jw.job.Phase, "line": line, "stage": stage})
}

// update changes the job's record by change while the job is the runner's own and running, and reports
// whether it changed it.
func (jw *jobWatch) update(change func(*state.Job) bool) bool {
	changed := false
	jw.s.Update(func(st *state.State) error {
		if j := st.Job(jw.job.ID); mine(j, jw.job) && j.Status == state.JobRunning {
			changed = change(j)
		}
		return nil
	})
	return changed
}

// finish reads what is left of the output w passed on, waits for the work queued, and returns the job's
// stages as its stages file has them, final now that the job has ended.
func (jw *jobWatch) finish(w *lane.Watcher) []lane.Stage {
	w.Flush()
	jw.mu.Lock()
	jw.closed = true
	close(jw.work)
	jw.mu.Unlock()
	<-jw.done
	stages, _ := lane.ReadStages(jw.stages)
	return stages
}
