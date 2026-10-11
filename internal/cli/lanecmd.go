package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

func init() {
	registerHidden("lane-run", cmdLaneRun)
}

// laneTick is how often a job's runner beats; tests shorten it.
var laneTick = lane.DefaultTick

// errNotWanted is a beat finding that the job is no longer running as far as the run's state says: it
// was cancelled, or is gone from the record.
var errNotWanted = errors.New("the job is no longer wanted")

// cmdLaneRun runs one job of a run to its end: `baton lane-run <ref> <job>`, where ref names the run, or a
// worker's sub-run (<run>/<phase>). The baton host starts it detached (lane.Spawn), with BATON_DIR,
// BATON_HOME and BATON_INSTANCE set; nobody runs it by hand, so it is left out of help. It takes the job
// from queued to running, runs it (package lane) while beating into the run's state, and records how it
// ended. A SIGTERM stops the job and brings its lane down.
func cmdLaneRun(args []string, io IO) int {
	if len(args) != 2 {
		fmt.Fprintln(io.Err, "usage: baton lane-run <ref> <job> (the baton host runs this)")
		return 2
	}
	ref, id := args[0], args[1]
	dir := io.Env("BATON_DIR")
	if dir == "" {
		fmt.Fprintln(io.Err, "baton: lane-run is started by the baton host, which says where the project is (BATON_DIR)")
		return 1
	}
	p, err := state.OpenProject(dir, io.Now)
	if err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n", err)
		return 1
	}
	s, err := p.ExistingRun(ref, io.Env("BATON_INSTANCE"))
	if err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n", err)
		return 1
	}

	var job state.Job
	if _, err := s.Update(func(st *state.State) error {
		j := st.Job(id)
		switch {
		case j == nil:
			return fmt.Errorf("run %s has no job %s", ref, id)
		case j.Status != state.JobQueued:
			return fmt.Errorf("job %s is %s, not queued", id, j.Status)
		}
		now := io.Now()
		j.Status, j.Started, j.Heartbeat, j.PID = state.JobRunning, now, now, os.Getpid()
		if j.Log == "" {
			j.Log = s.JobLog(id)
		}
		job = *j
		st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: job %s started — `%s` (%s)", id, j.Command, j.Phase))
		return nil
	}); err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n", err)
		return 1
	}
	s.Event("job_started", map[string]any{"job": id, "phase": job.Phase, "type": job.Kind, "command": job.Command, "commit": job.Commit, "pid": job.PID, "log": job.Log})

	res := runJob(s, job, io)
	ended := finishJob(s, job, res, io.Now())
	s.Event("job_finished", map[string]any{"job": id, "phase": job.Phase, "status": ended.Status, "exit": ended.Exit,
		"lane": ended.Lane, "reason": ended.Reason, "seconds": int(res.Took.Seconds())})
	return 0
}

// runJob runs the job through a lane runner that beats into the run's state.
func runJob(s *state.Store, job state.Job, io IO) lane.Result {
	log, err := lane.CreateLog(job.Log)
	if err != nil {
		return lane.Result{Status: lane.LaneError, Exit: -1, Reason: fmt.Sprintf("no log: %v", err)}
	}
	defer log.Close()
	cfg := config.Load(config.Root(io.Env), io.Env)
	timeout, err := cfg.JobTimeout()
	if err != nil {
		fmt.Fprintf(log, "[baton] %v; using %s\n", err, timeout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var host time.Time // the host's latest heartbeat
	r := &lane.Runner{
		Pool:    lane.Pool{Home: config.Root(io.Env), Root: s.Checkout},
		WorkDir: s.Root,
		Host:    io.Env("BATON_INSTANCE"),
		Log:     log,
		Now:     io.Now,
		Timeout: timeout,
		Tick:    laneTick,
		Beat: func(now time.Time) (time.Time, bool) {
			st, err := s.Update(func(st *state.State) error {
				j := st.Job(job.ID)
				if !mine(j, job) || j.Status != state.JobRunning {
					return errNotWanted
				}
				j.Heartbeat = now
				return nil
			})
			switch {
			case errors.Is(err, errNotWanted):
				return host, false
			case err == nil && st.Owner != nil:
				host = st.Owner.Heartbeat
			}
			return host, true
		},
		Started: func(n, pgid int) {
			s.Update(func(st *state.State) error {
				if j := st.Job(job.ID); mine(j, job) && j.Status == state.JobRunning {
					j.Lane, j.PGID = n, pgid
				}
				return nil
			})
		},
		LaneWait: func(since time.Time) {
			s.Update(func(st *state.State) error {
				if j := st.Job(job.ID); mine(j, job) && j.Status == state.JobRunning {
					j.LaneWait = since
				}
				return nil
			})
		},
	}
	return r.Run(ctx, lane.Job{ID: job.ID, Command: job.Command, Commit: job.Commit})
}

// mine reports whether j, as the run's state has it now, is the job this runner took up: a job dropped
// from the record, with its plan, is not, nor one that took its id since.
func mine(j *state.Job, job state.Job) bool {
	return j != nil && j.PID == job.PID && j.Started.Equal(job.Started)
}

// finishJob records how the job ended, unless something else ended it first (it was cancelled), and
// returns the job as recorded. The state may be busy for a moment, so it tries more than once.
func finishJob(s *state.Store, job state.Job, res lane.Result, now time.Time) state.Job {
	var ended state.Job
	for try := 0; try < 3; try++ {
		_, err := s.Update(func(st *state.State) error {
			j := st.Job(job.ID)
			if !mine(j, job) {
				ended = state.Job{ID: job.ID, Phase: job.Phase, Status: res.Status, Exit: res.Exit, Lane: res.Lane, Reason: res.Reason}
				return nil
			}
			if j.Status == state.JobRunning {
				j.Status, j.Exit, j.Reason = res.Status, res.Exit, res.Reason
			} else { // cancelled: its status stands, with the runner's account of the end
				if j.Exit == 0 {
					j.Exit = res.Exit
				}
				if j.Reason == "" {
					j.Reason = res.Reason
				}
			}
			if res.Lane != 0 {
				j.Lane = res.Lane
			}
			if j.Ended.IsZero() {
				j.Ended = now
			}
			ended = *j
			if line := decide.FinishedAside(*st, ended); line != "" {
				st.Run.Announce = append(st.Run.Announce, line)
			}
			return nil
		})
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	return ended
}
