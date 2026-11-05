package loop

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The model's own background shells, looked at all the time (v0.8.21). In a 74-phase campaign an until loop
// a subagent of the lead's model started polled another task's output for 22 hours, writing nothing, and kept
// every restart away: about 8 agent-hours of the human's. So, about once a minute, and at once when a look is
// owed (state.Runtime.Audit: a plan attached, the run resumed or carried on, a phase boundary of the lead's, a
// phase agent let go of, a restart that background work holds), the coordinator looks at the background work
// of every loop of the run, with workers: 0 too:
//
//   - it reads each shell's output file (its size, when it last changed, whether Claude Code wrote its end
//     line) and, where it can, its process tree's CPU since its last look, before any lock (taskStep);
//   - it names the shells with signals that they may do nothing more for the run (state.Suspects), and has
//     each reviewed by a model when a review is due (state.TaskReview.Due; review.go here): only the
//     reviewer's STOP stops one; KEEP looks again later; UNSURE, or no verdict, asks the task's owner to look
//     and tells the human, and stops nothing;
//   - it sees each stop through: TaskStop through baton's mod first, and, where the task still runs
//     taskStopWait later or no stop could be sent, its shell's process group, found among claude's children by
//     its command line (TaskProcs), never a process outside that claude's tree. Its output file stays;
//   - it names, once each, the long-running work it leaves alone: subagents, workflows and monitors, which it
//     never reviews or stops, and shells reviewed MaxReviews times (background_long).
//
// Nothing waits on any of it: the reviews run off the host's tick, and a look owed is made by the next one.

// taskEvery is how often the coordinator looks at the model's own background work.
const taskEvery = time.Minute

// taskStopWait is how long a task baton stops through its mod has to end before baton ends its process group
// itself; taskKillGrace how long those processes have to end before they are killed.
const (
	taskStopWait  = time.Minute
	taskKillGrace = 5 * time.Second
)

// TaskProcs finds the process of a background shell of the model's own, reads its process tree's CPU, and ends
// it where TaskStop could not (zero: reads and ends nothing, so tests never touch the machine). Claude Code
// runs each background command as a /bin/zsh -c (or another shell) that is a direct child of claude, leading
// a process group of its own, with the command inside `eval '…'` (spike 45); claude is the host's child.
type TaskProcs struct {
	Host  int // the host's pid
	List  func() ([]procs.Proc, error)
	Usage func(pgid int) (procs.Usage, error)
	Stop  func(pids []int, grace time.Duration) []int
}

// tasker is what the coordinator's look at background work keeps in memory: when it last looked, whether a
// step runs (and Ended waits for it), each shell's last CPU reading, the reviews this host has in flight,
// whether background work held a restart at the last tick, and how it calls the reviewer (tests stub it).
type tasker struct {
	at   time.Time
	busy atomic.Bool
	wg   sync.WaitGroup
	held bool

	mu      sync.Mutex
	cpu     map[string]cpuSeen
	touched map[string]bool // the shells the step under way read
	asks    map[string]time.Time
	call    func(ctx context.Context, s *state.Store, c Completion) (Answer, error)
	panic   time.Time
}

// cpuSeen is a shell's process tree's CPU reading, its group, and when it was read.
type cpuSeen struct {
	pgid int
	u    procs.Usage
	at   time.Time
}

// taskKey names task id of the loop whose run is s among the tasker's maps.
func taskKey(s *state.Store, id string) string { return s.Dir + "\x00" + id }

// taskLoop is a loop of the run as the look at background work reads it: its run and state; whose its tasks
// are, as the human reads it ("the lead", "P3's agent"); where its owner is told (its own run, or the lead's
// for a worker baton let go of: retired); and whether it is such a worker's.
type taskLoop struct {
	store   *state.Store
	st      state.State
	owner   string
	tell    *state.Store
	retired bool
}

// tasks is the coordinator's look at background work, from its duties: about once a minute while the plan
// runs, and at once when a look is owed (Runtime.Audit). restartDue says a restart of the lead (a newer baton,
// its phase's tier) is due: the first tick that finds background work holding it owes a look. The step runs
// off the tick where it reads the process table.
func (c *Coordinator) tasks(v host.View, s *state.Store, st state.State, restartDue bool) {
	held := restartDue && len(st.Run.BusyBackground()) > 0
	if held && !c.tk.held && st.Mode == state.ModeRunning {
		s.Update(func(st *state.State) error { st.Run.OweAudit(state.AuditRestart, v.Now); return nil })
		st.Run.OweAudit(state.AuditRestart, v.Now)
	}
	c.tk.held = held
	if st.Mode != state.ModeRunning {
		return
	}
	if st.Run.Audit == nil && !c.tk.at.IsZero() && v.Now.Sub(c.tk.at) < taskEvery {
		return
	}
	if !c.tk.busy.CompareAndSwap(false, true) {
		return
	}
	c.tk.at = v.Now
	step := func() {
		defer c.tk.busy.Store(false)
		defer c.taskPanic(v.Now, s)
		c.taskStep(v.Now, s)
	}
	if c.Tasks.List == nil {
		step() // no process table to read: files and state alone, on the tick
		return
	}
	c.tk.wg.Add(1)
	go func() {
		defer c.tk.wg.Done()
		step()
	}()
}

// taskPanic logs a panic of the look at background work, at most once a minute, and lets it go no further.
func (c *Coordinator) taskPanic(now time.Time, s *state.Store) {
	r := recover()
	if r == nil {
		return
	}
	c.Lead.logf("loop: background work: panic: %v", r)
	c.tk.mu.Lock()
	log := now.Sub(c.tk.panic) > time.Minute
	if log {
		c.tk.panic = now
	}
	c.tk.mu.Unlock()
	if log {
		s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "background work"})
	}
}

// audited tallies a look at background work owed at once, for background_audited.
type audited struct {
	suspects, reviewing, stopped, kept, long []string
}

// taskStep looks at the background work of every loop of the run s once (see above), at now.
func (c *Coordinator) taskStep(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil || st.Mode != state.ModeRunning {
		return
	}
	var why []string
	if a := st.Run.Audit; a != nil {
		why = slices.Clone(a.Why)
		s.Update(func(st *state.State) error { st.Run.TakeAudit(why); return nil })
	}
	shells, read := c.shells()
	var tally audited
	c.tk.mu.Lock()
	c.tk.touched = map[string]bool{}
	c.tk.mu.Unlock()
	for _, x := range c.taskLoops(s, st) {
		c.loopTasks(now, s, x, shells, read, &tally)
	}
	c.tk.mu.Lock()
	for key := range c.tk.cpu {
		if !c.tk.touched[key] {
			delete(c.tk.cpu, key) // a task no loop has any more
		}
	}
	c.tk.mu.Unlock()
	if len(why) > 0 {
		fields := map[string]any{"at": strings.Join(why, ","), "suspects": nonNil(tally.suspects), "reviewing": nonNil(tally.reviewing),
			"stopped": nonNil(tally.stopped), "kept": nonNil(tally.kept), "long": nonNil(tally.long)}
		s.Event("background_audited", fields)
		c.Lead.logf("loop: background work looked at (%s): %d suspect(s), %d reviewed now", strings.Join(why, ", "), len(tally.suspects), len(tally.reviewing))
	}
}

// nonNil is list, or an empty list for none, as an event logs it.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// taskLoops lists the loops of the run s (as st has it) whose background work is looked at: the lead's, its
// live phase agents', and the workers' it let go of whose loops saw a shell start (state.RetiredSuspects).
func (c *Coordinator) taskLoops(s *state.Store, st state.State) []taskLoop {
	loops := []taskLoop{{store: s, st: st, owner: "the lead", tell: s}}
	if c.Project == nil {
		return loops
	}
	live := map[string]bool{}
	for _, w := range st.LiveWorkers() {
		live[w.Ref] = true
	}
	for _, r := range c.Project.Workers(s.Ref) {
		_, phase, _ := strings.Cut(r.ID, "/")
		if !live[r.ID] && !startedShells(r.State) {
			continue
		}
		sub, err := c.Project.ExistingRun(r.ID, s.Instance)
		if err != nil {
			continue
		}
		x := taskLoop{store: sub, st: r.State, owner: phase + "'s agent", tell: sub}
		if !live[r.ID] {
			x.owner, x.tell, x.retired = "the lead", s, true
		}
		loops = append(loops, x)
	}
	return loops
}

// startedShells reports a loop that saw a background shell of its model's start that a Stop of its has not
// shown ended since.
func startedShells(st state.State) bool {
	for _, b := range st.Run.BackgroundStarts {
		if b.Type == state.TaskShell {
			return true
		}
	}
	return false
}

// taskRead is what the host read of one background shell: its output (stat), whether Claude Code wrote the
// line it ends a task's output with (over), and its shell's process group (pgid 0: not found).
type taskRead struct {
	stat state.TaskStat
	over bool
	pgid int
}

// loopTasks looks at the background work of loop x of the run lead (see above): reads each shell, reviews
// the suspects due a review, sees the stops under way through, and names long-running work once.
func (c *Coordinator) loopTasks(now time.Time, lead *state.Store, x taskLoop, shells []procs.Proc, read bool, tally *audited) {
	own := x.st.OwnTasks(now)
	reads := map[string]taskRead{}
	stats := map[string]state.TaskStat{}
	ended := map[string]bool{}
	for _, t := range own {
		if t.Kind != state.TaskShell {
			continue
		}
		id := t.Task.ID
		r := c.readTask(now, x.store, x.st.Run.TaskOutput(id), id, t.Command(), shells, read)
		reads[id] = r
		switch {
		case r.over:
			ended[id] = true // Claude Code wrote its end: the next Stop drops it
		case x.retired && (read && r.pgid == 0 || !read && r.stat.ModTime.IsZero()):
			ended[id] = true // a worker let go of: nothing shows it still runs
		default:
			stats[id] = r.stat
		}
	}
	c.stops(now, lead, x, own, reads, ended, read, tally)
	var sus []state.Suspect
	if x.retired {
		sus = state.RetiredSuspects(x.st, stats, c.Lead.Timing.TaskQuiet, now)
	} else {
		sus = state.Suspects(x.st, stats, c.Lead.Timing.TaskQuiet, now)
	}
	sus = slices.DeleteFunc(sus, func(s state.Suspect) bool { return ended[s.Task.ID] })
	for _, s := range sus {
		tally.suspects = append(tally.suspects, s.Task.ID)
		rv := x.st.Run.Reviews[s.Task.ID]
		if c.reviewLapsed(now, lead, x, s, rv) {
			continue
		}
		if due, kinds := rv.Due(s, now); due {
			if c.askReview(now, lead, x, s, kinds, reads[s.Task.ID].pgid) {
				tally.reviewing = append(tally.reviewing, s.Task.ID)
			}
			continue
		}
		if rv != nil && rv.Verdict == state.ReviewKeep {
			tally.kept = append(tally.kept, s.Task.ID)
		}
	}
	c.longTasks(now, lead, x, own, sus, ended, tally)
	if !x.retired {
		c.forgetReviews(x, own)
	}
}

// readTask reads background shell id of the loop whose run is s: its output file (output, "" when not
// known), and, where processes can be read (read), its shell among them by its command, and the CPU its
// tree used since the last look.
func (c *Coordinator) readTask(now time.Time, s *state.Store, output, id, command string, shells []procs.Proc, read bool) taskRead {
	var r taskRead
	if output != "" {
		if fi, err := os.Stat(output); err == nil {
			r.stat.Size, r.stat.ModTime = fi.Size(), fi.ModTime()
			r.over = endedOutput(output, fi.Size())
		}
	}
	if !read {
		return r
	}
	r.pgid = shellOf(shells, command)
	key := taskKey(s, id)
	c.tk.mu.Lock()
	defer c.tk.mu.Unlock()
	if c.tk.touched != nil {
		c.tk.touched[key] = true
	}
	if r.pgid == 0 || c.Tasks.Usage == nil {
		delete(c.tk.cpu, key)
		return r
	}
	u, err := c.Tasks.Usage(r.pgid)
	if err != nil || len(u) == 0 {
		delete(c.tk.cpu, key)
		return r
	}
	if prev, ok := c.tk.cpu[key]; ok && prev.pgid == r.pgid && now.After(prev.at) {
		r.stat.CPU, r.stat.Span = procs.CPUTime(u.Since(prev.u)), now.Sub(prev.at)
	}
	if c.tk.cpu == nil {
		c.tk.cpu = map[string]cpuSeen{}
	}
	c.tk.cpu[key] = cpuSeen{pgid: r.pgid, u: u, at: now}
	return r
}

// endedOutput reports whether a task's output file (of size bytes) ends with the line Claude Code writes as
// the task ends: `[exited with code N]`, or `[killed]` once TaskStop ended it (spike 45).
func endedOutput(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const tail = 128
	if size > tail {
		f.Seek(size-tail, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, tail))
	b = bytes.TrimRight(b, " \t\r\n")
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	line := string(bytes.TrimSpace(b))
	return line == "[killed]" || strings.HasPrefix(line, "[exited with code ") && strings.HasSuffix(line, "]")
}

// shells lists the shells claude runs for its model's background commands: the processes whose parent is a
// child of the host (claude), each leading its process group. read says the process table could be read.
func (c *Coordinator) shells() (out []procs.Proc, read bool) {
	t := c.Tasks
	if t.List == nil || t.Host <= 1 {
		return nil, false
	}
	all, err := t.List()
	if err != nil {
		return nil, false
	}
	claude := map[int]bool{}
	for _, p := range all {
		if p.PPID == t.Host {
			claude[p.PID] = true
		}
	}
	for _, p := range all {
		if claude[p.PPID] && p.PGID == p.PID && p.PID > 1 {
			out = append(out, p)
		}
	}
	return out, true
}

// shellOf is the process group of the shell among shells that runs command, the model's command as baton
// recorded it (clipped with "…" past its end): its script evals the command quoted, `eval '<command>'`, a `'`
// in it written `'"'"'`. 0 when none does, or more than one: two tasks running one command cannot be told apart.
func shellOf(shells []procs.Proc, command string) int {
	cmd, clipped := strings.CutSuffix(strings.TrimSpace(command), "…")
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return 0
	}
	quoted := strings.ReplaceAll(cmd, "'", `'"'"'`)
	found := 0
	for _, p := range shells {
		if !evals(p.Args, quoted, clipped) {
			continue
		}
		if found != 0 {
			return 0
		}
		found = p.PID
	}
	return found
}

// evals reports whether a shell's arguments eval quoted, a command quoted as the shell's script quotes it:
// whole, or starting so when it was clipped.
func evals(args []string, quoted string, clipped bool) bool {
	for _, a := range args {
		i := strings.Index(a, "eval '")
		if i < 0 {
			continue
		}
		body := strings.TrimLeft(a[i+len("eval '"):], " \t\r\n")
		if !strings.HasPrefix(body, quoted) {
			continue
		}
		if clipped || strings.HasPrefix(strings.TrimLeft(body[len(quoted):], " \t\r\n"), "'") {
			return true
		}
	}
	return false
}

// suspectOf is a suspect as baton says it (decide.TaskSuspect), its output file from the loop's run r.
func suspectOf(s state.Suspect, r state.Runtime) decide.TaskSuspect {
	return decide.TaskSuspect{ID: s.Task.ID, Type: s.Kind, Description: s.Description(), Phase: s.Phase, Age: s.Age, Declared: s.Declared,
		Signals: s.Signals, Output: r.TaskOutput(s.Task.ID), Command: s.Command()}
}

// ownedOf is task t of the loop as baton names it, with no signals: a stopped one, or long-running work.
func ownedOf(t state.OwnTask, r state.Runtime) decide.TaskSuspect {
	phase := ""
	if t.Start != nil {
		phase = t.Start.Phase
	}
	out := r.TaskOutput(t.Task.ID)
	if t.Start != nil && t.Start.Output != "" {
		out = t.Start.Output
	}
	return decide.TaskSuspect{ID: t.Task.ID, Type: t.Kind, Description: t.Description(), Phase: phase, Age: t.Age, Output: out, Command: t.Command()}
}

// stops sees through the stops under way of loop x's tasks (own, as read): one that ended, by TaskStop or
// by baton ending its processes, is said (background_stopped, announced with the reviewer's reason, and its
// owner told at its next hook); one still running taskStopWait after its stop was sent, or whose stop could
// not be sent, has its shell's process group ended (TaskProcs); one that cannot be ended so is said
// (background_stop_failed).
func (c *Coordinator) stops(now time.Time, lead *state.Store, x taskLoop, own []state.OwnTask, reads map[string]taskRead, ended map[string]bool, read bool, tally *audited) {
	for id, rv := range x.st.Run.Reviews {
		stop := rv.Stop
		if stop == nil || stop.Settled() {
			continue
		}
		i := slices.IndexFunc(own, func(t state.OwnTask) bool { return t.Task.ID == id })
		t := goneTask(id, *stop, now) // its start record went with a Stop that no longer lists it
		if i >= 0 {
			t = own[i]
		}
		switch {
		case i < 0 || ended[id]:
			via := state.StopViaTask
			if !stop.Killed.IsZero() {
				via = state.StopViaProcess
			}
			c.stopped(now, lead, x, t, *rv, via, tally)
		case !stop.Killed.IsZero() && now.Sub(stop.Killed) >= taskStopWait:
			c.stopFailed(now, lead, x, t, "its processes ran on after baton ended them")
		case !stop.Killed.IsZero():
		case stop.Command != 0 && now.Sub(stop.At) < taskStopWait:
			// TaskStop is on its way
		default:
			if stop.Command != 0 {
				bridge.Open(lead.Dir, lead.Now, nil).Void(lead.Instance, stop.Command)
			}
			c.killTask(now, lead, x, t, *rv, reads[id].pgid, read, tally)
		}
	}
}

// goneTask is task id as its stop names it, once no record of the loop's holds it any more.
func goneTask(id string, stop state.TaskStop, now time.Time) state.OwnTask {
	t := state.OwnTask{Task: state.Task{ID: id, Type: state.TaskShell, Description: stop.Description, Command: stop.Shell}, Kind: state.TaskShell,
		Start: &state.BackgroundStart{Type: state.TaskShell, Command: stop.Shell, Description: stop.Description, Phase: stop.Phase, Output: stop.Output,
			At: stop.Started}}
	if !stop.Started.IsZero() {
		t.Age = max(now.Sub(stop.Started), 0)
	}
	return t
}

// killTask ends the process group pgid of loop x's task t, which TaskStop did not end, or which had no stop
// sent: each process of its tree is asked to stop, then killed (procs.Stop), never a process outside it. The
// task counts stopped by baton (StopViaProcess) once all of them ended.
func (c *Coordinator) killTask(now time.Time, lead *state.Store, x taskLoop, t state.OwnTask, rv state.TaskReview, pgid int, read bool, tally *audited) {
	switch {
	case !read || c.Tasks.Usage == nil || c.Tasks.Stop == nil:
		c.stopFailed(now, lead, x, t, "TaskStop did not end it, and baton cannot read processes here")
		return
	case pgid == 0:
		c.stopFailed(now, lead, x, t, "TaskStop did not end it, and baton could not find its process")
		return
	}
	u, err := c.Tasks.Usage(pgid)
	if err != nil || len(u) == 0 {
		c.stopFailed(now, lead, x, t, "TaskStop did not end it, and baton could not read its processes")
		return
	}
	var pids []int
	for pid := range u {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	left := c.Tasks.Stop(pids, taskKillGrace)
	x.store.Update(func(st *state.State) error {
		if r := st.Run.Reviews[t.Task.ID]; r != nil && r.Stop != nil {
			r.Stop.Killed = now
		}
		return nil
	})
	c.Lead.logf("loop: background task %s: ended its process group %d (%d processes, %d left)", t.Task.ID, pgid, len(pids), len(left))
	if len(left) == 0 {
		rv.Stop.Killed = now
		c.stopped(now, lead, x, t, rv, state.StopViaProcess, tally)
	}
}

// stopped records that loop x's task t ended on its stop (via), logs background_stopped, tells the human
// with the reviewer's reason, and tells the task's owner at its next hook, naming the long-running work of
// its own left.
func (c *Coordinator) stopped(now time.Time, lead *state.Store, x taskLoop, t state.OwnTask, rv state.TaskReview, via string, tally *audited) {
	settled := false
	x.store.Update(func(st *state.State) error {
		if r := st.Run.Reviews[t.Task.ID]; r != nil && r.Stop != nil && !r.Stop.Settled() {
			r.Stop.Via, r.Stop.Ended, settled = via, now, true
		}
		return nil
	})
	if !settled {
		return
	}
	tally.stopped = append(tally.stopped, t.Task.ID)
	ts := ownedOf(t, x.st.Run)
	phase := ts.Phase
	fields := map[string]any{"task": t.Task.ID, "phase": phase, "command": clipLog(t.Command()), "signals": rv.Seen, "reason": rv.Reason,
		"model": rv.Model, "age_seconds": int(t.Age.Seconds()), "via": via, "owner": x.owner}
	if fi, err := os.Stat(ts.Output); err == nil {
		fields["quiet_for"] = int(now.Sub(fi.ModTime()).Seconds())
	}
	x.store.Event("background_stopped", fields)
	c.announce(lead, decide.TaskStoppedSay(ts, rv.Model, rv.Reason, x.owner))
	var left []string
	for _, o := range x.st.OwnTasks(now) {
		if o.Task.ID != t.Task.ID && o.Age >= state.LongTask {
			left = append(left, decide.LongName(ownedOf(o, x.st.Run)))
		}
	}
	text := decide.TaskStoppedLookIn(ts, rv.Model, rv.Reason, via == state.StopViaProcess, left)
	c.leaveLookIn(lead, lookLoop{store: x.tell}, state.LookIn{Kind: state.LookTaskStopped, At: now, Text: text, Say: lookSaid(x, decide.TaskStoppedSaid(ts))},
		map[string]any{"task": t.Task.ID, "phase": phase})
}

// lookSaid is the line that tells the human a look-in about loop x's background work reached its owner: the
// lead's hooks say it as they give it; for a phase agent, what baton announced already says it.
func lookSaid(x taskLoop, line string) string {
	if x.tell.Worker() != "" {
		return ""
	}
	return line
}

// stopFailed records that loop x's task t could not be stopped (why), and says so (background_stop_failed).
func (c *Coordinator) stopFailed(now time.Time, lead *state.Store, x taskLoop, t state.OwnTask, why string) {
	settled := false
	x.store.Update(func(st *state.State) error {
		if r := st.Run.Reviews[t.Task.ID]; r != nil && r.Stop != nil && !r.Stop.Settled() {
			r.Stop.Failed, settled = why, true
		}
		return nil
	})
	if !settled {
		return
	}
	ts := ownedOf(t, x.st.Run)
	x.store.Event("background_stop_failed", map[string]any{"task": t.Task.ID, "phase": ts.Phase, "command": clipLog(t.Command()), "why": why})
	c.announce(lead, decide.TaskStopFailedSay(ts, why))
}

// longTasks names, once each, the long-running work of loop x that baton leaves alone (background_long,
// told to the human): the model's subagents, workflows and monitors that have run state.LongTask
// (state.LongTasks), and the suspects reviewed MaxReviews times, kept.
func (c *Coordinator) longTasks(now time.Time, lead *state.Store, x taskLoop, own []state.OwnTask, sus []state.Suspect, ended map[string]bool, tally *audited) {
	type named struct {
		t        state.OwnTask
		reviewed bool
	}
	var out []named
	for _, t := range state.LongTasks(x.st, sus, now) {
		if !ended[t.Task.ID] && (x.st.Run.Reviews[t.Task.ID] == nil || !x.st.Run.Reviews[t.Task.ID].Long) {
			out = append(out, named{t: t})
		}
	}
	for _, s := range sus {
		if rv := x.st.Run.Reviews[s.Task.ID]; rv != nil && rv.Reviews >= state.MaxReviews && rv.Asking.IsZero() && rv.Stop == nil && !rv.Long {
			out = append(out, named{t: s.OwnTask, reviewed: true})
		}
	}
	for _, n := range out {
		tally.long = append(tally.long, n.t.Task.ID)
	}
	if len(out) == 0 {
		return
	}
	x.store.Update(func(st *state.State) error {
		for _, n := range out {
			st.Run.ReviewOf(n.t.Task.ID).Long = true
		}
		return nil
	})
	for _, n := range out {
		ts := ownedOf(n.t, x.st.Run)
		x.store.Event("background_long", map[string]any{"task": n.t.Task.ID, "type": n.t.Kind, "description": clipLog(n.t.Description()), "phase": ts.Phase,
			"age_seconds": int(n.t.Age.Seconds()), "reviewed": n.reviewed, "owner": x.owner})
		c.announce(lead, decide.TaskLongSay(ts, n.reviewed))
	}
}

// forgetReviews drops the review records of the tasks loop x no longer has (own), a stop under way aside.
func (c *Coordinator) forgetReviews(x taskLoop, own []state.OwnTask) {
	has := func(id string) bool {
		return slices.ContainsFunc(own, func(t state.OwnTask) bool { return t.Task.ID == id })
	}
	stale := false
	for id, rv := range x.st.Run.Reviews {
		if !has(id) && (rv.Stop == nil || rv.Stop.Settled()) {
			stale = true
		}
	}
	if stale {
		x.store.Update(func(st *state.State) error { st.Run.ForgetReviews(has); return nil })
	}
}
