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
	// Stages is the job's stages file (EnvStages), and Rerun the record of the red gate it reruns, a stages
	// file too (EnvRerun): both given to its command, "" for none.
	Stages, Rerun string
	// Here runs the command in the working tree itself (the runner's WorkDir), on the commit checked out
	// there (Commit, which nothing checks out): a gate that needs that tree. In the project's checkout it
	// holds no lane. In a worktree of its own (a phase agent's), it holds one of the pool's lanes while it
	// runs, as a gate in a lane does, and runs with BATON_LANE set to it, though it runs outside it: so
	// gates in lanes and in worktrees together run no more at once than the project's slots, and what a
	// lane keeps apart (a test database, a Compose project) stays apart.
	Here bool
	// QAPhase: the job is a QA phase's gate, given EnvQAPhase, so that every stage of it runs.
	QAPhase bool
}

// Result is how a job ended.
type Result struct {
	Status string
	Exit   int           // the command's exit code; -1 when it did not exit by itself, or never ran
	Lane   int           // the lane it ran in; 0 for none
	Reason string        // why, when the status is not the command's own verdict
	Took   time.Duration // how long the job ran while the machine was awake, its wait for a lane included
	Waited time.Duration // of Took, how long it waited for a lane
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
// it runs past Timeout, counted from when it got its lane (a wait for one is not running time), when the
// host stops beating for HostStale, when Beat says the job is no longer wanted, or when ctx ends (a
// SIGTERM). A stopped job's lane is brought down.
type Runner struct {
	Pool Pool
	// WorkDir is where a job with no commit (await), or one run Here, runs: the working tree of the
	// session it runs for, which is a worker's own worktree for a worker. "" is Pool.Root.
	WorkDir string
	Host    string    // the baton host the job runs for: a lane is brought up once per host
	Log     io.Writer // where every process's output goes, and the runner's own account; safe for concurrent use
	// Output is where the job's command's output goes instead of Log (a writer that passes it on to Log,
	// reading it as it goes); nil: Log. A lane's up and down write to Log.
	Output io.Writer
	Now    func() time.Time

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
	// LaneWait is told when the job starts to wait for a lane, every one of the pool's slots being in use
	// (since: when it started to), with how many jobs that began to wait before it wait still (ahead), and
	// when it stops waiting, with a lane or without (the zero time).
	LaneWait func(since time.Time, slots, ahead int)
	// Acquired is told when the job got its lane (at), whether it found one free or waited for it, and how
	// long it waited while the machine was awake; lane 0 for a job that needs none, as its command begins.
	// Its running time, and Timeout, count from then.
	Acquired func(at time.Time, lane int, waited time.Duration)

	ran, stale, waited time.Duration
	held               bool // the job has its lane, or needs none: Timeout counts
	prev, host         time.Time
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
	dir := r.WorkDir
	if dir == "" {
		dir = r.Pool.Root
	}
	switch {
	case j.Here && Key(dir) != Key(r.Pool.Root):
		res = r.hereInSlot(ctx, j, dir)
	case j.Here:
		r.say("job %s: %s, on %s in %s, with no lane", j.ID, j.Command, short(j.Commit), dir)
		r.got(0)
		res = r.command(ctx, 0, dir, j.env(r.Pool.env(0, "", "")), shell(j.Command))
	case j.Commit == "":
		r.say("job %s: %s, in %s", j.ID, j.Command, dir)
		r.got(0)
		res = r.command(ctx, 0, dir, j.env(r.Pool.env(0, "", "")), shell(j.Command))
	default:
		res = r.inLane(ctx, j)
	}
	r.account()
	res.Took, res.Waited = r.ran, r.waited
	if !r.held { // it never got a lane: all of it was waiting
		res.Waited = r.ran
	}
	took := (res.Took - res.Waited).Round(time.Second).String()
	if w := res.Waited.Round(time.Second); w > 0 {
		took += fmt.Sprintf(", %s waiting for a lane", w)
	}
	switch res.Status {
	case Passed:
		r.say("passed after %s", took)
	case Failed:
		r.say("failed (exit %d) after %s", res.Exit, took)
	default:
		r.say("%s after %s: %s", res.Status, took, res.Reason)
	}
	return res
}

// env is the environment of the job's command: env, a lane's, with the job's own variables of the gate
// protocol.
func (j Job) env(env []string) []string {
	env = append(env, EnvJob+"="+j.ID)
	if j.Stages != "" {
		env = append(env, EnvStages+"="+j.Stages)
	}
	if j.Rerun != "" {
		env = append(env, EnvRerun+"="+j.Rerun)
	}
	if j.QAPhase {
		env = append(env, EnvQAPhase+"=1")
	}
	return env
}

// begin starts the clock, and takes the host's heartbeat as it stands as the one to see move on.
func (r *Runner) begin() {
	r.defaults()
	r.prev = r.Now()
	if r.Beat != nil {
		r.host, _ = r.Beat(r.prev)
	}
}

// slot holds one of the pool's lanes for the job, waiting while every one is in use. It returns the lane,
// or why the job stopped as it waited, or why it got none.
func (r *Runner) slot(ctx context.Context) (*Slot, *Result, error) {
	n := r.Pool.Slots(ctx)
	slot, err := r.Pool.TryAcquire(n)
	if slot != nil {
		r.got(slot.N)
	}
	if slot != nil || err != nil {
		return slot, nil, err
	}
	since := r.Now()
	w, ahead := r.Pool.Wait(since)
	defer w.Done()
	if ahead > 0 {
		r.say("every lane is in use (%d); waiting for one, behind %d other job(s) waiting", n, ahead)
	} else {
		r.say("every lane is in use (%d); waiting for one", n)
	}
	r.laneWait(since, n, ahead)
	err, stop := r.supervise(ctx, func(c context.Context) error {
		var err error
		slot, err = r.Pool.Acquire(c, n)
		return err
	})
	r.laneWait(time.Time{}, n, 0)
	if stop != nil {
		if slot != nil {
			slot.Release()
		}
		return nil, stop, nil
	}
	if slot != nil {
		r.got(slot.N)
		r.say("lane %d came free after %s", slot.N, r.waited.Round(time.Second))
	}
	return slot, nil, err
}

// got starts the job's running time, and Timeout's count with it: it holds lane n (0: it needs none),
// having waited for it for as long as it has been at it so far.
func (r *Runner) got(n int) {
	_, now := r.account()
	r.waited, r.held = r.ran, true
	if r.Acquired != nil {
		r.Acquired(now, n, r.waited)
	}
}

// hereInSlot runs a gate Here in dir, a worktree outside the project's checkout, holding one of the
// pool's lanes while it runs (Job.Here). It checks nothing out in the lane and brings it neither up nor
// down: the gate runs on the tree, and what the lane's up started is the lane's.
func (r *Runner) hereInSlot(ctx context.Context, j Job, dir string) Result {
	slot, stop, err := r.slot(ctx)
	switch {
	case stop != nil:
		return *stop
	case err != nil:
		return Result{Status: LaneError, Exit: -1, Reason: fmt.Sprintf("no lane: %v", err)}
	}
	defer slot.Release()
	r.say("job %s: %s, on %s in %s, holding lane %d", j.ID, j.Command, short(j.Commit), dir, slot.N)
	res := r.command(ctx, slot.N, dir, j.env(r.Pool.env(slot.N, dir, j.Commit)), shell(j.Command))
	res.Lane = slot.N
	return res
}

// inLane runs a job in a lane of its own.
func (r *Runner) inLane(ctx context.Context, j Job) Result {
	slot, stop, err := r.slot(ctx)
	switch {
	case stop != nil:
		return *stop
	case err != nil:
		return Result{Status: LaneError, Exit: -1, Reason: fmt.Sprintf("no lane: %v", err)}
	}
	defer slot.Release()

	r.say("job %s: %s, on %s in lane %d (%s)", j.ID, j.Command, short(j.Commit), slot.N, slot.Dir)
	err, stop = r.supervise(ctx, func(c context.Context) error { return r.Pool.checkout(c, slot, j.Commit) })
	if stop != nil {
		return r.stopped(*stop, slot)
	}
	if err != nil {
		return Result{Status: LaneError, Exit: -1, Lane: slot.N, Reason: fmt.Sprintf("checking %s out in lane %d: %v", short(j.Commit), slot.N, err)}
	}

	env, s := r.Pool.env(slot.N, slot.Dir, j.Commit), script(slot.Dir)
	if s == "" {
		res := r.command(ctx, slot.N, slot.Dir, j.env(env), shell(j.Command))
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
			exit, stop, err := r.proc(ctx, slot.N, slot.Dir, env, []string{s, "up", slot.Dir}, r.Log)
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
	res := r.command(ctx, slot.N, slot.Dir, j.env(env), []string{s, "run", slot.Dir, j.Command})
	return r.stopped(res, slot)
}

// laneWait tells LaneWait, if set, that the job waits for one of slots lanes since since (zero: no
// longer), behind ahead others.
func (r *Runner) laneWait(since time.Time, slots, ahead int) {
	if r.LaneWait != nil {
		r.LaneWait(since, slots, ahead)
	}
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

// command runs the job's command, its output to Output, and judges it by its exit code.
func (r *Runner) command(ctx context.Context, lane int, dir string, env, argv []string) Result {
	out := r.Output
	if out == nil {
		out = r.Log
	}
	exit, stop, err := r.proc(ctx, lane, dir, env, argv, out)
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

// proc runs one process in a group of its own, its output to out, and waits for it under supervision. A
// process that is stopped gets SIGTERM, and is killed with everything left in its group after Grace.
// Whatever the process leaves running in its group is killed when it exits.
func (r *Runner) proc(ctx context.Context, lane int, dir string, env, argv []string, out io.Writer) (exit int, stop *Result, err error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
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
	if r.held && r.ran-r.waited >= r.Timeout {
		return &Result{Status: Timeout, Exit: -1, Reason: fmt.Sprintf("still running after %s", r.Timeout)}
	}
	return nil
}

// say adds a line of the runner's own to the log, with the time.
func (r *Runner) say(format string, args ...any) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	Say(r.Log, now(), format, args...)
}

// Say writes a line of baton's own into a job's log, with the time it says it: "[baton 14:02:11] …".
func Say(w io.Writer, now time.Time, format string, args ...any) {
	fmt.Fprintf(w, "[baton "+now.Format(time.TimeOnly)+"] "+format+"\n", args...)
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
