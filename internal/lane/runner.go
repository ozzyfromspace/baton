package lane

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// The statuses a job ends with: the command's verdict (passed, failed), or no verdict at all.
const (
	Passed      = "passed"
	Failed      = "failed"
	LaneError   = "lane_error"  // the lane could not run the command: not a test result
	Timeout     = "timeout"     // the command ran past the runner's Timeout: a failure
	Interrupted = "interrupted" // the job was stopped before the command finished
)

// Outcome is the status a command's exit code gives a job. 125, 126 and 127 are the shell's and the
// lane script's ways of saying the command never ran (it could not be set up, run, or found), which
// says nothing about the code under test.
func Outcome(exit int) string {
	switch exit {
	case 0:
		return Passed
	case 125, 126, 127:
		return LaneError
	}
	return Failed
}

// Job is a command to run.
type Job struct {
	ID      string
	Command string
	// Commit is the commit to check out in a lane and run the command on. Without one, the command runs
	// in the working tree itself, with no lane (a wait on something outside the project).
	Commit string
}

// Result is how a job ended.
type Result struct {
	Status string
	Exit   int           // the command's exit code; -1 when it did not exit by itself, or never ran
	Lane   int           // the lane it ran in; 0 for none
	Reason string        // why, when the status is not the command's own verdict
	Took   time.Duration // how long the job ran while the machine was awake
}

// Runner defaults.
const (
	DefaultTimeout   = 3 * time.Hour
	DefaultTick      = 15 * time.Second
	DefaultHostStale = 10 * time.Minute
	DefaultGrace     = 10 * time.Second
)

// Runner runs one job: it holds a lane, checks the commit out, brings the lane up if this host has not,
// and runs the command, every process in a group of its own. It beats every Tick, and stops the job when
// it runs past Timeout, when the host stops beating for HostStale, when Beat says the job is no longer
// wanted, or when ctx ends (a SIGTERM). A stopped job's lane is brought down.
type Runner struct {
	Pool Pool
	// WorkDir is where a job with no commit (await) runs: the working tree of the session it runs for,
	// which is a worker's own worktree for a worker. "" is Pool.Root.
	WorkDir string
	Host    string    // the baton host the job runs for: a lane is brought up once per host
	Log     io.Writer // where every process's output goes, and the runner's own account; safe for concurrent use
	Now     func() time.Time

	Timeout   time.Duration
	Tick      time.Duration
	HostStale time.Duration
	Grace     time.Duration // how long a stopped process group has to exit before it is killed
	// Gap is the longest pause between two beats that counts as time passing. A longer one is the machine
	// sleeping: it counts toward neither Timeout nor HostStale, so a laptop that slept through a gate does
	// not wake to a timeout. 0 is four Ticks.
	Gap time.Duration

	// Beat records that the job is alive (at now). It returns the host's latest heartbeat, and whether the
	// job is still wanted. nil: always wanted, and no host to watch.
	Beat func(now time.Time) (host time.Time, wanted bool)
	// Started is told the lane and the process group of each process the job starts.
	Started func(lane, pgid int)

	ran, stale time.Duration
	prev, host time.Time
}

func (r *Runner) defaults() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Log == nil {
		r.Log = io.Discard
	}
	for _, d := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&r.Timeout, DefaultTimeout}, {&r.Tick, DefaultTick}, {&r.HostStale, DefaultHostStale}, {&r.Grace, DefaultGrace}} {
		if *d.v <= 0 {
			*d.v = d.def
		}
	}
	if r.Gap < r.Tick {
		r.Gap = 4 * r.Tick
	}
}

// Run runs the job and returns how it ended.
func (r *Runner) Run(ctx context.Context, j Job) Result {
	r.begin()
	var res Result
	if j.Commit == "" {
		dir := r.WorkDir
		if dir == "" {
			dir = r.Pool.Root
		}
		r.say("job %s: %s, in %s", j.ID, j.Command, dir)
		res = r.command(ctx, 0, dir, r.Pool.env(0, "", ""), shell(j.Command))
	} else {
		res = r.inLane(ctx, j)
	}
	r.account()
	res.Took = r.ran
	switch res.Status {
	case Passed:
		r.say("passed after %s", res.Took.Round(time.Second))
	case Failed:
		r.say("failed (exit %d) after %s", res.Exit, res.Took.Round(time.Second))
	default:
		r.say("%s after %s: %s", res.Status, res.Took.Round(time.Second), res.Reason)
	}
	return res
}

// begin starts the clock, and takes the host's heartbeat as it stands as the one to see move on.
func (r *Runner) begin() {
	r.defaults()
	r.prev = r.Now()
	if r.Beat != nil {
		r.host, _ = r.Beat(r.prev)
	}
}

// inLane runs a job in a lane of its own.
func (r *Runner) inLane(ctx context.Context, j Job) Result {
	n := r.Pool.Slots(ctx)
	slot, err := r.Pool.TryAcquire(n)
	if slot == nil && err == nil {
		r.say("every lane is in use (%d); waiting for one", n)
		var stop *Result
		if err, stop = r.supervise(ctx, func(c context.Context) error {
			slot, err = r.Pool.Acquire(c, n)
			return err
		}); stop != nil {
			if slot != nil {
				slot.Release()
			}
			return *stop
		}
	}
	if err != nil {
		return Result{Status: LaneError, Exit: -1, Reason: fmt.Sprintf("no lane: %v", err)}
	}
	defer slot.Release()

	r.say("job %s: %s, on %s in lane %d (%s)", j.ID, j.Command, short(j.Commit), slot.N, slot.Dir)
	err, stop := r.supervise(ctx, func(c context.Context) error { return r.Pool.checkout(c, slot, j.Commit) })
	if stop != nil {
		return r.stopped(*stop, slot)
	}
	if err != nil {
		return Result{Status: LaneError, Exit: -1, Lane: slot.N, Reason: fmt.Sprintf("checking %s out in lane %d: %v", short(j.Commit), slot.N, err)}
	}

	env, s := r.Pool.env(slot.N, slot.Dir, j.Commit), script(slot.Dir)
	if s == "" {
		res := r.command(ctx, slot.N, slot.Dir, env, shell(j.Command))
		return r.stopped(res, slot)
	}
	if by := r.Pool.upBy(slot.N); by == "" || by != r.Host {
		if by != "" {
			// Another host brought the lane up (two runs share a checkout's lanes, and a baton that restarts
			// itself is a new host): what that up started is not this host's, so it comes down first.
			r.say("lane %d: brought up by host %s; %s down first", slot.N, by, Script)
			if err := r.Pool.down(slot.N, r.Log); err != nil {
				r.say("%v", err)
			}
		}
		for try := 1; ; try++ {
			r.say("lane %d: %s up", slot.N, Script)
			exit, stop, err := r.proc(ctx, slot.N, slot.Dir, env, []string{s, "up", slot.Dir})
			if stop != nil {
				return r.stopped(*stop, slot)
			}
			if err == nil && exit == 0 {
				host := r.Host
				if host == "" {
					host = "?"
				}
				os.WriteFile(r.Pool.upPath(slot.N), []byte(host+"\n"), 0o644)
				break
			}
			if try == 2 {
				return Result{Status: LaneError, Exit: -1, Lane: slot.N, Reason: fmt.Sprintf("%s up failed twice (%s)", Script, exitText(exit, err))}
			}
			r.say("lane %d: up failed (%s); trying once more", slot.N, exitText(exit, err))
		}
	}
	res := r.command(ctx, slot.N, slot.Dir, env, []string{s, "run", slot.Dir, j.Command})
	return r.stopped(res, slot)
}

// stopped brings the lane down after a job was stopped, so whatever its up started does not outlive the
// host that wanted it.
func (r *Runner) stopped(res Result, slot *Slot) Result {
	res.Lane = slot.N
	if res.Status == Interrupted {
		if err := r.Pool.down(slot.N, r.Log); err != nil {
			r.say("%v", err)
		}
	}
	return res
}

// command runs the job's command and judges it by its exit code.
func (r *Runner) command(ctx context.Context, lane int, dir string, env, argv []string) Result {
	exit, stop, err := r.proc(ctx, lane, dir, env, argv)
	switch {
	case stop != nil:
		stop.Exit = exit
		return *stop
	case err != nil:
		return Result{Status: LaneError, Exit: -1, Reason: fmt.Sprintf("could not run %s: %v", argv[0], err)}
	case Outcome(exit) == LaneError:
		return Result{Status: LaneError, Exit: exit, Reason: fmt.Sprintf("the command did not run (%s)", exitText(exit, nil))}
	}
	return Result{Status: Outcome(exit), Exit: exit}
}

// proc runs one process in a group of its own and waits for it under supervision. A process that is
// stopped gets SIGTERM, and is killed with everything left in its group after Grace. Whatever the process
// leaves running in its group is killed when it exits.
func (r *Runner) proc(ctx context.Context, lane int, dir string, env, argv []string) (exit int, stop *Result, err error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, r.Log, r.Log
	cmd.WaitDelay = r.Grace // a background child holding the output open cannot hold the job up
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		return -1, nil, err
	}
	if r.Started != nil {
		r.Started(lane, cmd.Process.Pid)
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		killGroup(cmd)
		close(done)
	}()
	_, stop = r.supervise(ctx, func(c context.Context) error {
		select {
		case <-done:
			return nil
		case <-c.Done():
		}
		signalGroup(cmd, false)
		select {
		case <-done:
		case <-time.After(r.Grace):
			signalGroup(cmd, true)
			<-done
		}
		return nil
	})
	return cmd.ProcessState.ExitCode(), stop, nil
}

// supervise runs step and beats until it returns. When the job must stop (see check), or ctx ends, it
// cancels step's context and waits for step to wind down. It returns step's error, and why the job was
// stopped (nil if it was not).
func (r *Runner) supervise(ctx context.Context, step func(context.Context) error) (error, *Result) {
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- step(sctx) }()
	tick := time.NewTicker(r.Tick)
	defer tick.Stop()
	ended := ctx.Done()
	var stop *Result
	for {
		select {
		case err := <-done:
			return err, stop
		case <-ended:
			ended = nil
			if stop == nil {
				stop = &Result{Status: Interrupted, Exit: -1, Reason: "the job was stopped"}
				cancel()
			}
		case <-tick.C:
			if stop == nil {
				if stop = r.check(); stop != nil {
					cancel()
				}
			}
		}
	}
}

// account adds the time since it last looked to the time the job has run, unless the machine slept
// through it. It returns that time, and the clock's reading.
func (r *Runner) account() (time.Duration, time.Time) {
	now := r.Now()
	d := now.Sub(r.prev)
	r.prev = now
	if d < 0 || d > r.Gap {
		d = 0
	}
	r.ran += d
	return d, now
}

// check accounts for the time since the last beat and beats. It returns why the job must stop, or nil.
func (r *Runner) check() *Result {
	d, now := r.account()
	if r.Beat != nil {
		host, wanted := r.Beat(now)
		if !wanted {
			return &Result{Status: Interrupted, Exit: -1, Reason: "the job was cancelled"}
		}
		if host.After(r.host) {
			r.host, r.stale = host, 0
		} else {
			r.stale += d
		}
		if r.stale >= r.HostStale {
			return &Result{Status: Interrupted, Exit: -1, Reason: fmt.Sprintf("the baton host has not been heard from for %s", r.stale.Round(time.Second))}
		}
	}
	if r.ran >= r.Timeout {
		return &Result{Status: Timeout, Exit: -1, Reason: fmt.Sprintf("still running after %s", r.Timeout)}
	}
	return nil
}

// say adds a line of the runner's own to the log.
func (r *Runner) say(format string, args ...any) {
	fmt.Fprintf(r.Log, "[baton] "+format+"\n", args...)
}

func exitText(exit int, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case exit < 0:
		return "killed by a signal"
	}
	return fmt.Sprintf("exit %d", exit)
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
