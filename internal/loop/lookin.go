package loop

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Look-ins (state.LookIn) tell a session what its gates and landings do while they run, as information:
// baton stops nothing for them, and nothing waits on them. A gate's first failure its runner records on
// the job (Job.Failing), and the session's hooks give it. The rest the coordinator finds here, about once
// a minute (lookIns), and leaves for the session each concerns, once per job and kind (Job.LookedIn):
//
//   - a gate running past decide.LookSlowFactor times the slowest earlier passing run of its command, and
//     Timing.SlowAfter more (past_history), and one at decide.LookTimeoutShare of verify_timeout
//     (near_timeout): to its owner, the lead or the phase agent that queued it, with its elapsed time, the
//     earlier runs and how long its log has been still;
//   - landings that have waited Timing.HeldAfter behind a gate in the lead's checkout (landings_held): to
//     the lead, which and since when;
//   - a file in a second landing conflict of the run (conflicting_file): to the lead, as the merge queue
//     hands the landing back (conflicted), once a file;
//   - what finished phases left running, while the machine has no room and a gate runs past its history
//     (leftovers): to the gate's owner (leftover.go).
//
// Each goes to its session by the hooks a result takes (a tool call, a stop, a hand-back, a prompt, a
// brief), whatever phase the run is on, and an idle session is woken for it (Loop.deliver); never through
// the inbox (state.Mail), which drops what is about a phase not in hand.

// lookInEvery is how often the coordinator looks for look-ins.
const lookInEvery = time.Minute

// lookLoop is a loop of the run as the look-ins read it: its run, and its state as loaded.
type lookLoop struct {
	store *state.Store
	st    state.State
}

// lookIns finds the look-ins the gates and landings of the run st, in s, call for (see above), and leaves
// each for its session. It looks about once a minute, while the plan runs.
func (c *Coordinator) lookIns(v host.View, s *state.Store, st state.State) {
	if !c.lookAt.IsZero() && v.Now.Sub(c.lookAt) < lookInEvery {
		return
	}
	c.lookAt = v.Now
	if st.Mode != state.ModeRunning {
		return
	}
	loops := []lookLoop{{store: s, st: st}}
	for _, r := range c.liveSubs(s, st) {
		if sub, err := c.Project.Run(r.ID, s.Instance); err == nil {
			loops = append(loops, lookLoop{store: sub, st: r.State})
		}
	}
	passes := passingRuns(loops)
	for _, x := range loops {
		for _, j := range x.st.Jobs {
			if j.Status == state.JobRunning && j.Kind != state.JobAwait && j.LaneWait.IsZero() {
				c.gateLookIns(v.Now, s, x, j, passes[j.Command])
			}
		}
	}
	c.leftoverLookIns(v.Now, s, st, loops, passes)
	c.heldLookIn(v.Now, s, st)
}

// passingRuns is, by command, how long each passing gate of loops took, oldest first: the history a running
// gate is held to. A gate's time counts from when its command began (gateBegan).
func passingRuns(loops []lookLoop) map[string][]time.Duration {
	type run struct {
		ended time.Time
		took  time.Duration
	}
	by := map[string][]run{}
	for _, x := range loops {
		for _, j := range x.st.Jobs {
			from := gateBegan(j)
			if j.Status != state.JobPassed || j.Kind == state.JobAwait || from.IsZero() || !j.Ended.After(from) {
				continue
			}
			by[j.Command] = append(by[j.Command], run{j.Ended, j.Ended.Sub(from)})
		}
	}
	out := map[string][]time.Duration{}
	for cmd, runs := range by {
		slices.SortFunc(runs, func(a, b run) int { return a.ended.Compare(b.ended) })
		for _, r := range runs {
			out[cmd] = append(out[cmd], r.took)
		}
	}
	return out
}

// maxEarlier is how many earlier runs a look-in names, the latest.
const maxEarlier = 5

// gateLookIns leaves the look-ins gate j, running in loop x, calls for at now: past its history (earlier,
// the passing runs of its command, oldest first), and near verify_timeout. lead is the lead's run.
func (c *Coordinator) gateLookIns(now time.Time, lead *state.Store, x lookLoop, j state.Job, earlier []time.Duration) {
	t := c.Lead.Timing
	from := gateBegan(j)
	if from.IsZero() || !now.After(from) {
		return
	}
	ran := now.Sub(from)
	log := j.Log
	if log == "" {
		log = x.store.JobLog(j.ID)
	}
	still := stillFor(now, log)
	if pastHistory(t, ran, earlier) && !slices.Contains(j.LookedIn, state.LookSlow) {
		slowest, named := slices.Max(earlier), earlier[max(0, len(earlier)-maxEarlier):]
		c.leaveLookIn(lead, x, state.LookIn{Kind: state.LookSlow, Job: j.ID, At: now,
			Text: decide.GateSlow(j, ran, named, still, log, t.JobTimeout), Say: decide.GateSlowSaid(j, ran, slowest)},
			map[string]any{"phase": j.Phase, "command": j.Command, "seconds": int(ran.Seconds()), "slowest_seconds": int(slowest.Seconds()),
				"runs": len(earlier), "log_still_seconds": lookSeconds(still)})
	}
	if t.JobTimeout > 0 && !slices.Contains(j.LookedIn, state.LookTimeout) && ran >= time.Duration(float64(t.JobTimeout)*decide.LookTimeoutShare) {
		c.leaveLookIn(lead, x, state.LookIn{Kind: state.LookTimeout, Job: j.ID, At: now,
			Text: decide.GateNearTimeout(j, ran, t.JobTimeout, still, log), Say: decide.GateNearTimeoutSaid(j, ran, t.JobTimeout)},
			map[string]any{"phase": j.Phase, "command": j.Command, "seconds": int(ran.Seconds()), "timeout_seconds": int(t.JobTimeout.Seconds()),
				"log_still_seconds": lookSeconds(still)})
	}
}

// heldLookIn leaves the lead a look-in once its gate in the checkout (state.HereGate, of the run st in s)
// has held a landing for Timing.HeldAfter: the run's landings it holds, by phase and since when, and how
// many of other baton sessions' (those with no gate of their own there).
func (c *Coordinator) heldLookIn(now time.Time, s *state.Store, st state.State) {
	after := c.Lead.Timing.HeldAfter
	g := st.HereGate()
	if after <= 0 || g == nil || g.Status != state.JobRunning || slices.Contains(g.LookedIn, state.LookHeld) {
		return
	}
	why := heldByGate(g.ID)
	var held []decide.HeldLanding
	var oldest time.Time
	waits := func(w state.Worker) bool {
		if w.Status != state.WorkerLanding || w.Merge == nil || w.Merge.Deferred != why || w.Merge.DeferredAt.IsZero() {
			return false
		}
		if oldest.IsZero() || w.Merge.DeferredAt.Before(oldest) {
			oldest = w.Merge.DeferredAt
		}
		return true
	}
	for _, w := range st.Workers {
		if waits(w) {
			held = append(held, decide.HeldLanding{Phase: w.Phase, Since: w.Merge.DeferredAt})
		}
	}
	others := 0
	if c.Project != nil {
		for _, r := range c.Project.Others(s.Ref) {
			if r.State.HereGate() != nil {
				continue // its landings wait on its own gate, whose id may read the same
			}
			for _, w := range r.State.Workers {
				if waits(w) {
					others++
				}
			}
		}
	}
	if oldest.IsZero() || now.Sub(oldest) < after {
		return
	}
	var phases []string
	for _, h := range held {
		phases = append(phases, h.Phase)
	}
	c.leaveLookIn(s, lookLoop{store: s, st: st}, state.LookIn{Kind: state.LookHeld, Job: g.ID, At: now,
		Text: decide.LandingsWait(*g, now.Sub(gateBegan(*g)), held, others, now), Say: decide.LandingsWaitSaid(*g, held, others)},
		map[string]any{"phase": g.Phase, "command": g.Command, "landings": phases, "others": others, "waited_seconds": int(now.Sub(oldest).Seconds())})
}

// heldByGate is why the merge queue holds a landing while gate id runs in the lead's checkout
// (Merge.Deferred): the gate reads the checkout's files.
func heldByGate(id string) string { return fmt.Sprintf("a gate runs in the checkout (job %s)", id) }

// conflicted counts a landing of phase that conflicted with the run's branch in paths, in the run s
// (state.CountConflict), and leaves the lead a look-in for the paths in their second conflict, each once,
// while phases not started name them (conflict.go): one file every phase edits serializes the phases, and
// a replan can still give those files of their own. Each such path cues that replan at the lead's next
// phase boundary (state.CueConflict).
func (c *Coordinator) conflicted(now time.Time, s *state.Store, phase string, paths []string) {
	if len(paths) == 0 {
		return
	}
	pl, named := conflictNames(s, paths)
	replans := c.Fleet != nil && c.Fleet.AutoReplan
	var second, told []string
	var by, naming map[string][]string
	s.Update(func(st *state.State) error {
		second = state.CountConflict(st, phase, paths)
		if told, naming = unstartedNaming(*st, pl, named, second); len(told) > 0 {
			by = maps.Clone(st.Conflicted)
			state.CueConflict(st, told, naming)
			state.AddLookIn(st, state.LookIn{Kind: state.LookConflict, At: now, Text: decide.FileConflicts(told, by, naming, replans), Say: decide.FileConflictsSaid(told, naming, replans)})
		}
		return nil
	})
	if len(second) == 0 {
		return
	}
	if len(told) == 0 {
		c.Lead.logf("loop: %v conflicted in a second landing: no phase not started names it, so the lead is not told", second)
		return
	}
	phases := map[string][]string{}
	for _, p := range told {
		phases[p] = by[p]
	}
	c.Lead.logf("loop: %v conflicted in a second landing: told the lead", told)
	s.Event("look_in", map[string]any{"type": state.LookConflict, "paths": told, "phases": phases, "named": naming, "replan": replans})
}

// leaveLookIn leaves li in the run of loop x (state.AddLookIn), unless it was left before, and logs it there
// (look_in, with fields). One for a phase agent is told to the human in the lead's run (lead) as it is
// left; the lead's own, as its hook gives it.
func (c *Coordinator) leaveLookIn(lead *state.Store, x lookLoop, li state.LookIn, fields map[string]any) {
	left := false
	x.store.Update(func(st *state.State) error {
		left = state.AddLookIn(st, li)
		return nil
	})
	if !left {
		return
	}
	fields["type"] = li.Kind
	if li.Job != "" {
		fields["job"] = li.Job
	}
	x.store.Event("look_in", fields)
	who := "the lead"
	if phase := x.store.Worker(); phase != "" {
		who = phase + "'s agent"
		if li.Say != "" {
			c.announce(lead, li.Say+" → telling "+who)
		}
	}
	c.Lead.logf("loop: look-in (%s) for %s: %s", li.Kind, who, li.Say)
}

// lookInsDue lists the kinds of the look-ins due to the session of the run st now (state.LookInsDue), one
// a look-in, and the ids that tell the line typed for them from the next (deliver).
func lookInsDue(st state.State) (kinds []string, ids string) {
	for _, j := range st.UntoldFailures() {
		kinds = append(kinds, state.LookFailing)
		ids += " fail:" + j.ID
	}
	for _, li := range st.LeftLookIns() {
		kinds = append(kinds, li.Kind)
		ids += " look:" + li.Kind + ":" + li.Job + "@" + li.At.Format(time.RFC3339Nano)
	}
	return kinds, ids
}

// gateBegan is when job j's command began: when it got its lane (Job.Ran), or, for a job recorded without
// it, when it started.
func gateBegan(j state.Job) time.Time {
	if !j.Ran.IsZero() {
		return j.Ran
	}
	return j.Started
}

// lookSeconds is d in whole seconds, as the event log has it: -1 for a duration not known (negative).
func lookSeconds(d time.Duration) int {
	if d < 0 {
		return -1
	}
	return int(d.Seconds())
}

// stillFor is how long the log at path has not been written to, at now: -1 when it cannot be read.
func stillFor(now time.Time, path string) time.Duration {
	fi, err := os.Stat(path)
	if path == "" || err != nil {
		return -1
	}
	if d := now.Sub(fi.ModTime()); d > 0 {
		return d
	}
	return 0
}
