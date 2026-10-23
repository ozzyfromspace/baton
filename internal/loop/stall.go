package loop

import (
	"maps"
	"time"

	"github.com/ozzyfromspace/baton/internal/board"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A run that stops moving says so. Each loop's watchdog (watch) keeps its own conversation going, and
// rightly stays quiet while it waits on something that ends by itself: a compaction, a job, a phase agent,
// the human. A run can stop all the same, each loop waiting on something that never comes (on 2026-10-13
// a phase agent's red gate left a run so for three hours, every watchdog quiet). So the coordinator looks
// at the run as a whole, about once a minute (stalls): with the plan running and phases left, nothing busy,
// and nothing in the run moved for Timing.StallAfter, it pushes the human one notice naming what each
// phase left waits on (run_stalled). It is no escalation (that would hold the lead's deliveries and
// reminders): the run goes on as it was, and movement re-arms the alarm.
//
//   - Movement: in the lead's run and each live phase agent's sub-run, a hook (LastActivity), a turn's
//     start or end, the human taking part, a compaction over, a phase done, a job ended; in the lead's, a
//     landing, and a worker changing how it stands.
//   - Busy, so no alarm: a tool call in flight; the model's own background work, younger than
//     Timing.BackgroundIdle; a job running whose runner beats: an await (it waits on something outside
//     that ends by itself, bounded by verify_timeout), a gate waiting for a lane, and a gate whose log grew
//     within StallAfter or whose process tree used CPU since the alarm's last look. A gate quiet in both
//     (or in its log, where its processes cannot be read) is a hung gate, and named so.
//   - Never: a run not running, or with nothing left to do; the account's usage limit; the weekly
//     wind-down once nothing is under way (winddown.go: what is under way should finish, and is watched);
//     a dialog open (the human is told of it already). The quiet is counted from the machine's last wake
//     (Loop.wokeAt), from the moment a usage limit lifts, and from the wind-down's end.

// stallEvery is how often the coordinator looks at whether the run has stopped moving.
const stallEvery = time.Minute

// logSeen is what the stall alarm saw of a running gate at its last look: its log (logMark, as the hung
// check reads it) and since when it has not moved, and the process group it runs with the CPU its tree
// had used (nil: not read, or no reading).
type logSeen struct {
	log  logMark
	grew time.Time
	pgid int
	cpu  procs.Usage
}

// stalls pushes the human one notice when the run st, in s, has stopped moving (see above), and re-arms
// once it has moved since. It looks about once a minute.
func (c *Coordinator) stalls(v host.View, s *state.Store, st state.State) {
	after := c.Lead.Timing.StallAfter
	if after <= 0 || !c.stallAt.IsZero() && v.Now.Sub(c.stallAt) < stallEvery {
		return
	}
	c.stallAt = v.Now
	pl, err := s.LoadPlan()
	if err != nil {
		return
	}
	subs := c.liveSubs(s, st)
	loops := []state.State{st}
	for _, r := range subs {
		loops = append(loops, r.State)
	}
	quiet := c.readJobs(v.Now, loops)
	since := latest(movedAt(st), c.Lead.wokeAt, c.Lead.limited(st), woundUpAt(st))
	for _, r := range subs {
		since = latest(since, movedAt(r.State))
	}
	if sl := st.Run.Stalled; sl != nil && since.After(sl.Since) {
		// The run moved since baton said it had stopped: the alarm is armed again.
		s.Update(func(st *state.State) error {
			if x := st.Run.Stalled; x != nil && x.At.Equal(sl.At) {
				st.Run.Stalled = nil
			}
			return nil
		})
		st.Run.Stalled = nil
	}
	if st.Run.Stalled != nil || since.IsZero() || v.Now.Sub(since) < after || c.stillWhy(v.Now, st, pl, loops, quiet) != "" {
		return
	}
	b := board.Build(board.Input{State: st, Plan: pl, HavePlan: true, Subs: subs, Now: v.Now})
	var waits []string
	phases := map[string]string{}
	for _, step := range b.Steps {
		for _, p := range step.Phases {
			if p.Stand.Kind == decide.StandDone {
				continue
			}
			stand := p.Stand.Board(v.Now)
			if d, ok := quiet[p.Stand.Job]; ok {
				stand += ", " + decide.LogQuiet(d)
			}
			phases[p.ID] = stand
			waits = append(waits, p.ID+": "+stand)
		}
	}
	lead := b.Lead.Board(v.Now)
	text := decide.RunStalled(v.Now.Sub(since), waits, lead)
	s.Update(func(st *state.State) error {
		st.Run.Stalled = &state.Stall{Since: since, At: v.Now}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "run_stalled", Text: text, At: v.Now})
		return nil
	})
	c.Lead.logf("loop: the run has not moved since %s: told the human", since.Format(time.RFC3339))
	s.Event("run_stalled", map[string]any{"quiet": v.Now.Sub(since).Round(time.Second).String(), "phases": phases, "lead": lead})
}

// liveSubs are the sub-runs of the live workers of the plan attached to st (s's run), as they stand.
func (c *Coordinator) liveSubs(s *state.Store, st state.State) []state.RunInfo {
	if c.Project == nil {
		return nil
	}
	live := map[string]bool{}
	for _, w := range st.LiveWorkers() {
		if w.Generation == st.Generation {
			live[w.Ref] = true
		}
	}
	var out []state.RunInfo
	for _, r := range c.Project.Workers(s.Ref) {
		if live[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// movedAt is when the loop whose run is st last moved: a hook, a turn's start or end, the human taking
// part, a compaction over, a phase done, a job ended; in the lead's run, a landing and a worker's change of
// standing too (a worker's sub-run has neither).
func movedAt(st state.State) time.Time {
	r := st.Run
	t := latest(r.LastActivity, r.TurnStarted, r.LastStop, r.HumanAt, r.Compaction.Finished)
	for _, ps := range st.Phases {
		t = latest(t, ps.DoneAt)
	}
	for _, j := range st.Jobs {
		t = latest(t, j.Ended)
	}
	for _, l := range st.Landed {
		t = latest(t, l.At)
	}
	for _, w := range st.Workers {
		t = latest(t, w.Since)
	}
	return t
}

// readJobs looks at each gate running in loops (the lead's run and its live sub-runs), and returns those
// that look hung, by job, with how long their log has not moved: a log quiet for Timing.StallAfter, and a
// process tree that used no CPU since the last look (or that cannot be read: the log alone decides). A
// log is taken to move when the alarm first sees it: a host that has just started knows nothing of
// before. The tree is read only while the log stands still, so at most once a look (a minute) per job.
// An await is never hung here, whatever its log does, nor is a gate waiting for a lane: each waits on
// something that ends by itself, and counts as busy while its runner beats.
func (c *Coordinator) readJobs(now time.Time, loops []state.State) map[string]time.Duration {
	seen := map[string]logSeen{}
	quiet := map[string]time.Duration{}
	for _, st := range loops {
		for _, j := range st.Jobs {
			if j.Status != state.JobRunning || j.Kind == state.JobAwait || !j.LaneWait.IsZero() {
				continue
			}
			was, ok := c.logs[j.ID]
			at := logSeen{log: markOf(j.Log), grew: now, pgid: j.PGID}
			if ok && was.log == at.log {
				at.grew = was.grew
				at.cpu = c.cpuOf(j.PGID)
			}
			seen[j.ID] = at
			d := now.Sub(at.grew)
			if d < c.Lead.Timing.StallAfter {
				continue
			}
			if at.cpu != nil && (was.cpu == nil || was.pgid != at.pgid || !maps.Equal(was.cpu, at.cpu)) {
				continue // its processes worked since the last look (or are read for the first time)
			}
			quiet[j.ID] = d
		}
	}
	c.logs = seen // a job that ended is forgotten
	return quiet
}

// cpuOf reads the CPU time the process tree of group pgid has used so far, or nil without a reading (no
// group yet, no runners, a system that cannot read it, or no process running).
func (c *Coordinator) cpuOf(pgid int) procs.Usage {
	if pgid == 0 || c.Lead.Runners == nil {
		return nil
	}
	if u, err := c.Lead.Runners.Usage(pgid); err == nil && len(u) > 0 {
		return u
	}
	return nil
}

// stillWhy says why the run st cannot be stalled now ("" when it can): it is not running, has nothing
// left to do, waits on the account's usage limit, on the weekly reset with nothing under way (the
// wind-down), or on a dialog (a phase agent's too, named by its phase:
// the human is told of it already), or something in one of its loops is busy (quiet: the running gates
// that look hung, readJobs).
func (c *Coordinator) stillWhy(now time.Time, st state.State, pl plan.Plan, loops []state.State, quiet map[string]time.Duration) string {
	switch {
	case st.Mode != state.ModeRunning:
		return "the run is " + st.Mode
	case len(state.Remaining(st, pl)) == 0:
		return "no phase is left"
	case now.Before(c.Lead.limited(st)):
		return gateUsage
	case st.WoundDown() && len(state.UnderWay(st, pl)) == 0:
		return windWait
	case st.Run.Dialogs.LeadOpen():
		return gateDialog
	case st.Run.Dialogs.AnyOpen():
		d, _ := st.Run.Dialogs.Front()
		return "a dialog of " + d.Phase + "'s agent is open"
	}
	for _, x := range loops {
		if len(x.Run.InFlight(now)) > 0 {
			return "a tool call runs"
		}
		if busy := x.Run.BusyBackground(); len(busy) > 0 && now.Sub(backgroundSince(busy, x.Run.LastStop)) < c.Lead.Timing.BackgroundIdle {
			return "background work runs"
		}
		for _, j := range x.Jobs {
			if _, hung := quiet[j.ID]; j.Status == state.JobRunning && !hung && now.Sub(latest(j.Heartbeat, j.Started)) <= c.Lead.Timing.JobStale {
				return "job " + j.ID + " runs"
			}
		}
	}
	return ""
}
