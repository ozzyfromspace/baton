package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The coordinator runs the run's phase agents: background subagents of the lead's session that baton's
// mod starts in the lead's claude (docs/research/visible-workers.md), each on a phase that can go ahead
// beside the lead's, in a git worktree of its own, on a branch of its own made from the run's branch. It
// starts one whenever a slot is free, a phase can start, and the machine has room for another (Fleet.next,
// room.go), one at a time: the roster has the worker first, then its sub-run is made, and then the mod is
// asked to start its agent (bridge.Spawn). The worktree hook places the agent in the phase's worktree and
// links it to the sub-run; from there its own hooks brief it and hold its stops, its loop (Agent) runs its
// jobs, and the mod's ack says it started. A spawn not acked within Timing.AgentStart is taken back. A
// worker the run no longer counts on (its run dropped, its plan replaced, its phase gone from the plan, its
// work landed) is ended: the mod stops its agent, and it is retired (RetireWorker): its uncommitted work is
// saved and its worktree removed. Its branch goes too, unless it holds commits that never landed. Phase
// agents live in the lead's claude and end with it: a worker whose agent ended with the host is suspended
// (Ended), and the next host for the run starts an agent for it again in its worktree, or retires it when
// that host runs no workers. All of this runs off the lead's tick, one step at a time, since git takes a
// while.

// fleetEvery is how often the coordinator looks over the run's phase agents.
const fleetEvery = time.Second

// gitTimeout bounds each git command run for a worker: the merge queue's rebase, and its landing.
const gitTimeout = 2 * time.Minute

// launchRetry is how long the coordinator waits before it starts a phase agent again after one failed to
// start.
const launchRetry = 5 * time.Minute

// stopWait is how long an agent baton ends has to stop (the mod's ack of its stop) before baton retires
// its worker all the same.
const stopWait = 30 * time.Second

// Fleet is what the coordinator needs to run phase agents. A coordinator without one runs none
// (workers: 0, or a machine with no room for one), and retires those its run has on record.
type Fleet struct {
	Max      int     // agents at once: workers' number, or with auto what the machine allows (config.AutoWorkers)
	UsageMax float64 // no new agent above this percent of the five-hour allowance (worker_usage_max; 0: no check)
	MaxAuto  int     // the decisions a phase may make without the human before they review them (max_auto_decisions; 0: no cap)
	// Machine reads the machine (nil: machine.Read): no agent starts while it has no room for another
	// (room.go).
	Machine func() machine.Reading
	// Go runs the coordinator's work on the agents off the lead's tick (nil: on a goroutine of its own).
	Go func(fn func())

	git landGit // the merge queue's git (nil: gitx)
}

func (f *Fleet) run(fn func()) {
	if f.Go != nil {
		f.Go(fn)
		return
	}
	go fn()
}

// worktree is where worker phase of run works: under baton's home, outside the checkout, by its real
// path (claude names the session's project after its working directory, and resolves it).
func (c *Coordinator) worktree(checkout, run, phase string) (string, error) {
	parent := filepath.Join(c.Home, "worktrees", lane.Key(checkout), run)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(parent); err == nil {
		parent = real
	}
	return filepath.Join(parent, phase), nil
}

// launchable says why no worker may start or be resumed now, or "" if one may: the run must be running,
// with nothing waiting on the human, and no session waiting on a usage limit.
func launchable(st state.State, now time.Time) string {
	switch {
	case st.Mode != state.ModeRunning:
		return "the run is " + st.Mode
	case st.Blocked != nil:
		return "the run is blocked"
	case st.ReviewDue != "":
		return "the run waits on a review of its decisions"
	case st.Usage != nil && now.Before(st.Usage.Limited):
		return "a session waits on a usage limit"
	}
	return ""
}

// next is the phase a new worker would take up ("" if none waits for one), and why none starts now ("" if
// one does): the run must be running, under Max workers of its plan, with a phase that can go ahead.
// held names what holds a phase that waits back: no room on the machine (r.busy), gates waiting for lanes
// (r.lanes), landings that keep conflicting, or no room under worker_usage_max (state.HoldBusy,
// HoldLanes, HoldConflicts, HoldUsage); "" for none.
func (f *Fleet) next(st state.State, pl plan.Plan, now time.Time, r room) (phase, why, held string) {
	if why := launchable(st, now); why != "" {
		return "", why, ""
	}
	if st.RunBranch == "" {
		return "", "the plan was attached with no branch checked out", ""
	}
	if n := planWorkers(st); n >= f.Max {
		return "", fmt.Sprintf("%d of %d workers run already", n, f.Max), ""
	}
	if phase = state.NextWorker(st, pl); phase == "" {
		return "", "no other phase can start", ""
	}
	u := st.Usage
	switch {
	case r.busy != "":
		return phase, r.busy, state.HoldBusy
	case r.lanes != "":
		return phase, r.lanes, state.HoldLanes
	case conflicted(st) != "":
		return phase, conflicted(st), state.HoldConflicts
	case u != nil && f.UsageMax > 0 && u.UsedPct >= f.UsageMax && (u.Resets.IsZero() || now.Before(u.Resets)):
		return phase, fmt.Sprintf("%.0f%% of the five-hour usage is used (worker_usage_max %.0f%%)", u.UsedPct, f.UsageMax), state.HoldUsage
	}
	return phase, "", ""
}

// planWorkers counts the live workers of the plan attached now: a worker of a replaced plan is on its
// way out.
func planWorkers(st state.State) int {
	n := 0
	for _, w := range st.LiveWorkers() {
		if w.Generation == st.Generation {
			n++
		}
	}
	return n
}

// unwanted says why the run no longer counts on worker e (nil: it is not on the roster), or "" while it
// does. A worker of a plan the run has replaced is never a worker of the plan attached now, whatever its
// phase's id.
func unwanted(st state.State, pl plan.Plan, e *state.Worker) string {
	switch {
	case e == nil || e.Generation < st.Generation:
		return "the run's plan was replaced"
	case e.Status == state.WorkerLanded:
		return "its work landed"
	case !e.Live():
		return "it was taken off the run"
	case st.Mode == state.ModeIdle:
		return "the run was dropped"
	case pl.Index(e.Phase) < 0:
		return "its phase is no longer in the plan"
	}
	if ps := st.Phases[e.Phase]; ps == nil || ps.Status != state.PhaseWorking || ps.Worker != e.Ref {
		return "its phase is no longer the worker's"
	}
	return ""
}

// fleet starts a step of the coordinator's work on the phase agents, unless one is still going, at most
// once a second. A host with no Fleet steps only while its run st has workers on record, to retire them,
// or once a plan attached owes a sweep of those other runs left behind; otherwise it does nothing at all.
func (c *Coordinator) fleet(v host.View, s *state.Store, st state.State) {
	if c.Project != nil {
		c.planAttached(s, st)
	}
	f := c.Fleet
	if f == nil && st.Hold != nil { // an earlier host's: no worker starts in this one
		s.Update(func(st *state.State) error { st.Hold = nil; return nil })
	}
	idle := f == nil && (c.Project == nil || len(st.LiveWorkers()) == 0 && !c.sweepOwed)
	if idle || v.Now.Sub(c.fleetAt) < fleetEvery || !c.fleetBusy.CompareAndSwap(false, true) {
		return
	}
	if c.since.IsZero() {
		c.since = v.Now
	}
	c.fleetAt = v.Now
	sweep := c.sweepOwed
	c.sweepOwed = false
	c.fleetWG.Add(1)
	run := func(fn func()) { go fn() }
	if f != nil {
		run = f.run
	}
	run(func() {
		defer c.fleetWG.Done()
		defer c.fleetBusy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				c.Lead.logf("loop: workers: panic: %v", r)
				if v.Now.Sub(c.fleetPanic) > time.Minute {
					c.fleetPanic = v.Now
					s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "workers"})
				}
			}
		}()
		c.step(v.Now, s, sweep)
	})
}

// step looks over the run's phase agents once: first, when a plan was attached (sweep), the workers other
// runs left behind; then what the mod reported, the agents this host started, the merge queue and the
// answers to their questions, the roster's workers no agent of this host works on, and whether a new one
// can start. With no Fleet, it only retires the roster's workers: workers are off in this host, and their
// phases go back to the lead.
func (c *Coordinator) step(now time.Time, s *state.Store, sweep bool) {
	if sweep {
		c.sweep(now, s)
	}
	st, err := s.Load()
	if err != nil {
		return
	}
	pl, _ := s.LoadPlan() // none once the run was dropped
	if c.Fleet == nil {
		for _, e := range st.LiveWorkers() {
			if e.RetiringBy(s.Instance, now) {
				continue // another baton process is retiring it
			}
			why := unwanted(st, pl, &e)
			if why == "" {
				why = "workers are off in this session"
			}
			c.retire(now, s, e, why)
		}
		return
	}
	b := c.bridge(s)
	if !c.claimed && b.Claim(s.Instance) == nil {
		c.claimed = true // the commands an earlier host left for a claude that is gone are void
	}
	c.reports(now, s, b)
	if st, err = s.Load(); err != nil {
		return
	}
	c.usage(now, s, st)
	c.look(now)
	for _, w := range c.launched() {
		c.watch(now, s, b, st, pl, w)
	}
	for _, w := range c.launched() {
		w.agent.Tick(now)
	}
	c.merge(now, s)
	c.relay(now, s)
	if st, err = s.Load(); err != nil {
		return
	}
	for _, e := range st.LiveWorkers() {
		if c.runs(e.Ref) || e.RetiringBy(s.Instance, now) {
			continue
		}
		if why := unwanted(st, pl, &e); why != "" {
			c.retire(now, s, e, why)
		} else if e.Status != state.WorkerHeld && c.mayLaunch(now, s, st) == "" && c.noRoom == "" {
			why := e.Why
			if why == "" {
				why = "its agent ended with the baton session that ran it"
			}
			c.respawn(now, s, b, e, why)
		}
	}
	if st, err = s.Load(); err != nil {
		return
	}
	phase, why, held := "", c.mayLaunch(now, s, st), ""
	if why == "" {
		phase, why, held = c.Fleet.next(st, pl, now, room{busy: c.noRoom, lanes: lanes(now, s, st)})
	}
	c.hold(now, s, st, phase, why, held)
	if phase != "" && why == "" {
		c.spawn(now, s, b, st, pl, phase)
	}
}

// bridge is the run's side of the bridge to baton's mod, which logs to the run.
func (c *Coordinator) bridge(s *state.Store) *bridge.Bridge {
	b := bridge.Open(s.Dir, s.Now, func(kind string, fields map[string]any) { s.Event(kind, fields) })
	b.Drop = decide.ReportKept
	return b
}

// mayLaunch says why this host may start no phase agent now, or "" if it may, room on the machine aside
// (Coordinator.noRoom). The lead's session must have started first, and baton's mod must have said hello
// in its claude (modWait); one agent starts at a time, so that each worktree Claude Code asks for belongs
// to one phase.
func (c *Coordinator) mayLaunch(now time.Time, s *state.Store, st state.State) string {
	switch {
	case c.closing || c.stopping.Load():
		return "the baton session is ending"
	case c.pending.Load():
		return "baton restarts on a newer version first"
	case st.Run.LastActivity.Before(c.since):
		return "the lead's session has not started yet"
	case c.off != "":
		return c.off
	case now.Before(c.retryAt):
		return "a phase agent failed to start a moment ago"
	case !gitx.Usable(s.Checkout):
		return "the project is not a git repository"
	}
	for _, w := range c.launched() {
		if w.spawn != 0 {
			return "the phase agent on " + w.phase + " is starting"
		}
	}
	if why := c.modWait(now, s, st); why != "" {
		return why
	}
	return launchable(st, now)
}

// spawn starts a phase agent on phase. The roster has its worker first, so that nothing else takes the
// phase; then its sub-run is made, and only then is the mod asked to start its agent (issue). Its worktree
// is made by the worktree hook, as the agent starts.
func (c *Coordinator) spawn(now time.Time, s *state.Store, b *bridge.Bridge, st state.State, pl plan.Plan, phase string) {
	base := gitx.RevParse(s.Checkout, "refs/heads/"+st.RunBranch)
	if base == "" {
		c.later(now, s, phase, fmt.Sprintf("the run's branch %s is not there", st.RunBranch))
		return
	}
	dir, err := c.worktree(s.Checkout, s.Ref, phase)
	if err != nil {
		c.later(now, s, phase, err.Error())
		return
	}
	e := state.Worker{Phase: phase, Ref: state.WorkerRef(s.Ref, phase), Worktree: dir, Branch: "baton/" + s.Ref + "/" + phase, Base: base, Assigned: now}
	if _, err := s.Update(func(st *state.State) error { return state.Assign(st, pl, e, now) }); err != nil {
		c.Lead.logf("loop: %s: no worker: %v", phase, err) // the lead took it up meanwhile
		return
	}
	e.Status, e.Since, e.Generation = state.WorkerStarting, now, st.Generation
	sub, err := c.subRun(s, pl, e)
	if err != nil {
		c.retire(now, s, e, "baton could not make its run: "+err.Error())
		c.later(now, s, phase, err.Error())
		return
	}
	c.issue(now, s, b, e, sub, pl, "")
}

// issue asks the mod to start the agent of worker e, whose sub-run is sub, and keeps it as one of this
// host's (why: why it starts again, "" for the first time).
func (c *Coordinator) issue(now time.Time, s *state.Store, b *bridge.Bridge, e state.Worker, sub *state.Store, pl plan.Plan, why string) {
	title := ""
	if i := pl.Index(e.Phase); i >= 0 {
		title = pl.Phases[i].Title
	}
	cmd, err := b.Issue(bridge.Command{Kind: bridge.Spawn, Instance: s.Instance, Phase: e.Phase, Name: e.Phase,
		Description: decide.AgentDescription(e.Phase, title), Prompt: decide.AgentBegin(e.Phase)})
	if err != nil {
		c.retire(now, s, e, "baton could not ask its mod to start its agent: "+err.Error())
		c.later(now, s, e.Phase, err.Error())
		return
	}
	w := &worker{phase: e.Phase, entry: e, store: sub, agent: c.newAgent(e.Phase, sub), launched: now, spawn: cmd.ID}
	c.mu.Lock()
	if c.workers == nil {
		c.workers = map[string]*worker{}
	}
	c.workers[e.Phase] = w
	c.mu.Unlock()
	c.Lead.logf("loop: %s: asked the mod to start its agent (command %d)", e.Phase, cmd.ID)
	fields := map[string]any{"phase": e.Phase, "worktree": e.Worktree, "branch": e.Branch, "base": e.Base, "command": cmd.ID}
	if why == "" {
		s.Event("worker_spawned", fields)
		c.announce(s, fmt.Sprintf("baton: a phase agent takes up %s, in a worktree of its own on the branch %s", phaseName(pl, e.Phase), e.Branch))
		return
	}
	fields["why"] = why
	s.Event("worker_resumed", fields)
	c.announce(s, fmt.Sprintf("baton: a phase agent takes %s up again, in its worktree: %s", e.Phase, why))
}

// reports takes what the mod reported since the last step (bridge.Take): the acks of the commands this
// host gave it, and its agents' turns. (Its hello is serial mode's to read, modWait, and `baton mod
// report` counts the lead's compactions as they come.)
func (c *Coordinator) reports(now time.Time, s *state.Store, b *bridge.Bridge) {
	reps, err := b.Take()
	if err != nil {
		c.Lead.logf("loop: the mod's reports: %v", err)
		return
	}
	for _, r := range reps {
		switch r.Kind {
		case bridge.Ack:
			for _, w := range c.launched() {
				switch r.Command {
				case w.spawn:
					c.spawnAcked(now, s, b, w, r)
				case w.stop:
					w.stop = 0
				case w.sending:
					c.sendAcked(s, w, r)
				}
			}
		case bridge.Turn:
			c.turned(s, r)
		}
	}
}

// spawnAcked deals with the mod's ack of the spawn of w's agent (r; overdue: none came in time). The agent
// the worktree hook placed in the phase's worktree (Worker.AgentID) is the phase's, and at work. An agent
// that started without being placed works somewhere else, and is ended; with none placed, none started.
func (c *Coordinator) spawnAcked(now time.Time, s *state.Store, b *bridge.Bridge, w *worker, r bridge.Report) {
	w.spawn = 0
	st, err := s.Load()
	if err != nil {
		return
	}
	e := st.Entry(w.entry)
	if e == nil || !e.Live() {
		return // the run no longer counts on it: watch ends it
	}
	placed := e.AgentID
	switch {
	case placed != "" && (r.Interrupted || r.OK && r.Agent == placed):
		b.Track(placed, w.phase) // an interrupted spawn's ack made it none of baton's agents
		w.entry.AgentID = placed
		w.agent.in = line{c, s, w} // its loop reaches it from now on
		c.setEntry(s, w.entry, func(x *state.Worker) {
			x.Status, x.Since = state.WorkerWorking, now
			if x.Started.IsZero() {
				x.Started = now
			}
		})
		s.Event("worker_started", map[string]any{"phase": w.phase, "agent": placed, "after": now.Sub(w.launched).Round(time.Second).String()})
		c.announce(s, fmt.Sprintf("baton: the phase agent on %s is at work", w.phase))
	case r.OK && r.Agent != "":
		w.entry.AgentID = r.Agent // the mod tracks it: it is stopped, and forgotten
		c.end(now, s, b, w, "its agent started outside its phase's worktree")
		c.later(now, s, w.phase, "its agent started outside its phase's worktree")
	default:
		why := "its agent did not start"
		if r.Error != "" {
			why += ": " + r.Error
		}
		c.end(now, s, b, w, why)
		c.later(now, s, w.phase, why)
	}
}

// turned takes the mod's report r of the end of a turn of one of this host's agents. It closes the turn in
// the agent's sub-run, unless its hooks did: a turn that ends interrupted or in an error has no
// SubagentStop (a turn that started since is another). A turn that ended in an error or a refusal is
// recorded as the lead's is at its StopFailure (Runtime.LastError), for the agent's watchdog to retry or
// escalate as it does the lead's: an agent's StopFailure names the error, and the mod's report the refusal.
// One baton did not end itself that was interrupted is given one message to go on (decide.AgentGoOn).
func (c *Coordinator) turned(s *state.Store, r bridge.Report) {
	for _, w := range c.launched() {
		if w.entry.AgentID == "" || w.entry.AgentID != r.Agent {
			continue
		}
		w.store.Update(func(st *state.State) error {
			fresh := st.Run.LastError == nil || st.Run.LastError.At.Before(st.Run.TurnStarted)
			switch {
			case r.Reason == "refusal" && fresh:
				st.Run.LastError = &state.StopError{Error: state.ErrRefusal, Category: r.Refusal, At: r.At}
			case r.Reason == "refusal":
				st.Run.LastError.Error, st.Run.LastError.Category = state.ErrRefusal, r.Refusal
			case r.Reason == "error" && fresh:
				st.Run.LastError = &state.StopError{Error: "unknown", Details: "its turn ended in an error the mod saw, with no StopFailure", At: r.At}
			}
			if st.Run.TurnOpen && !st.Run.TurnStarted.After(r.At) {
				st.Run.TurnOpen, st.Run.LastStop = false, r.At
			}
			return nil
		})
		if r.Reason == "aborted" && w.ending == "" && w.stop == 0 && w.agent.in != nil {
			w.store.Event("agent_interrupted", map[string]any{"agent": r.Agent})
			if err := c.send(s, w, decide.AgentGoOn(w.phase)); err != nil {
				c.Lead.logf("loop: %s: telling its agent to go on: %v", w.phase, err)
			}
		}
	}
}

// watch looks at the agent of w: whether the run still counts on it, whether it has started in time, and
// whether one baton is ending has stopped.
func (c *Coordinator) watch(now time.Time, s *state.Store, b *bridge.Bridge, st state.State, pl plan.Plan, w *worker) {
	e := st.Entry(w.entry)
	switch why := unwanted(st, pl, e); {
	case w.ending != "":
		if w.stop == 0 || now.Sub(w.endAt) > stopWait {
			c.finish(now, s, b, w)
		}
	case why != "":
		c.end(now, s, b, w, why)
	case e.Status == state.WorkerSuspended:
		c.afresh(now, s, b, w, *e) // its agent is gone for good, or the human asked for a fresh one
	case w.spawn != 0 && now.Sub(w.launched) > c.Lead.Timing.AgentStart:
		// Taken back, the spawn cannot start an agent any more: its worktree would be refused.
		b.Void(s.Instance, w.spawn)
		c.spawnAcked(now, s, b, w, bridge.Report{Interrupted: true, Error: fmt.Sprintf("the mod did not start it within %s", c.Lead.Timing.AgentStart)})
	}
}

// end ends w's agent, for why: a spawn still open is taken back (bridge.Void), and an agent is stopped
// (bridge.Stop: TaskStop). Once the mod has acked its stop, or after stopWait, its worker is retired
// (finish).
func (c *Coordinator) end(now time.Time, s *state.Store, b *bridge.Bridge, w *worker, why string) {
	w.ending, w.endAt = why, now
	w.agent.in = nil // nothing more is sent to it
	c.Lead.logf("loop: %s: ending its phase agent: %s", w.phase, why)
	if w.spawn != 0 {
		b.Void(s.Instance, w.spawn)
		w.spawn = 0
	}
	if agent := c.agentOf(s, w); agent != "" {
		if cmd, err := b.Issue(bridge.Command{Kind: bridge.Stop, Instance: s.Instance, Phase: w.phase, Agent: agent}); err == nil {
			w.stop = cmd.ID
			return
		}
	}
	c.finish(now, s, b, w)
}

// finish retires the worker of an agent baton ended (end): it is none of baton's agents any more, so that
// nothing is ever sent to it again (a message would start it again), its loop lets it go, and its worker is
// retired.
func (c *Coordinator) finish(now time.Time, s *state.Store, b *bridge.Bridge, w *worker) {
	c.unregister(w)
	w.agent.Ended()
	if agent := c.agentOf(s, w); agent != "" {
		b.Forget(agent)
	}
	entry := w.entry
	if st, err := s.Load(); err == nil {
		if e := st.Entry(w.entry); e != nil {
			entry = *e
		}
	}
	c.retire(now, s, entry, w.ending)
}

// agentOf is the agent working for w, as the roster has it (the worktree hook records it), or as this host
// knows it.
func (c *Coordinator) agentOf(s *state.Store, w *worker) string {
	if st, err := s.Load(); err == nil {
		if e := st.Entry(w.entry); e != nil && e.AgentID != "" {
			return e.AgentID
		}
	}
	return w.entry.AgentID
}

// later puts off starting phase agents after one failed to start for why.
func (c *Coordinator) later(now time.Time, s *state.Store, phase, why string) {
	c.retryAt = now.Add(launchRetry)
	c.Lead.logf("loop: %s: no worker for now: %s", phase, why)
	s.Event("worker_deferred", map[string]any{"phase": phase, "why": why, "until": c.retryAt.UTC().Format(time.RFC3339)})
	c.announce(s, fmt.Sprintf("baton: no phase agent on %s for now (%s) → baton tries again at %s", phase, why, c.retryAt.Local().Format("15:04")))
}

// subRun makes worker e's sub-run, claimed by its instance: the run's plan narrowed to its phase, in hand.
func (c *Coordinator) subRun(s *state.Store, pl plan.Plan, e state.Worker) (*state.Store, error) {
	sub, err := c.Project.NewWorker(s.Ref, e.Phase, s.Instance, e.Worktree)
	if err != nil {
		return nil, err
	}
	only, err := pl.Only(e.Phase)
	if err != nil {
		return nil, err
	}
	if err := sub.SavePlan(only); err != nil {
		return nil, err
	}
	_, err = sub.Update(func(st *state.State) error { *st = state.AttachWorker(*st, e.Phase); return nil })
	return sub, err
}

// respawn starts an agent again for the roster's worker e, on which no agent of this host works: one whose
// agent ended with the lead's claude (suspended), or one an earlier baton left. The new agent takes the
// phase up in its worktree as the worker left it; the agent before it is none of baton's any more. Once its
// sub-run is claimed, the worker must still be on the roster as e was read, and not retiring: another baton
// process may be retiring it meanwhile (RetireWorker), and a worker is never both started again and
// retired.
func (c *Coordinator) respawn(now time.Time, s *state.Store, b *bridge.Bridge, e state.Worker, why string) {
	hook(c.resumeStep, "read")
	sub, err := c.Project.NewWorker(s.Ref, e.Phase, s.Instance, e.Worktree)
	if err != nil {
		c.Lead.logf("loop: %s: not started again yet: %v", e.Phase, err) // still claimed by the host that ran it
		return
	}
	hook(c.resumeStep, "claimed")
	var held string
	_, err = s.Update(func(st *state.State) error {
		w := st.Entry(e)
		switch {
		case w == nil || !w.Live() || w.Status != e.Status || !w.Since.Equal(e.Since):
			held = "it changed since it was looked at"
		case w.RetiringBy(s.Instance, now):
			held = w.Retirer + " is retiring it"
		}
		if held != "" {
			return errKept
		}
		w.Status, w.Since, w.Why, w.Ended, w.Merge, w.AgentID = state.WorkerStarting, now, "", now, nil, ""
		w.Retiring, w.Retirer = time.Time{}, ""
		return nil
	})
	if err != nil && held == "" {
		held = err.Error()
	}
	if held != "" {
		sub.Update(func(st *state.State) error { state.Release(st, sub.Instance); return nil })
		c.Lead.logf("loop: %s: not started again: %s", e.Phase, held)
		return
	}
	hook(c.resumeStep, "guarded")
	if e.AgentID != "" {
		b.Forget(e.AgentID) // gone with its claude: a message would only start it again
	}
	pl, _ := s.LoadPlan()
	e.Status, e.Since, e.AgentID = state.WorkerStarting, now, ""
	c.issue(now, s, b, e, sub, pl, why)
}

// afresh lets go of w, whose agent a fresh one replaces in its worktree (e, its entry, is suspended, and
// says why): its agent is gone for good, as a message could not start it again, or the human asked for a
// fresh one (baton run). The agent before it is stopped, should it still run, and is none of baton's any
// more; the roster's suspended worker, which no agent of this host works on now, is then taken up again
// as soon as an agent may start (step: respawn).
func (c *Coordinator) afresh(now time.Time, s *state.Store, b *bridge.Bridge, w *worker, e state.Worker) {
	c.unregister(w)
	w.agent.in = nil
	w.agent.Ended()
	if agent := c.agentOf(s, w); agent != "" {
		b.Issue(bridge.Command{Kind: bridge.Stop, Instance: s.Instance, Phase: w.phase, Agent: agent})
		b.Forget(agent)
	}
	c.Lead.logf("loop: %s: a fresh agent takes it up: %s", w.phase, e.Why)
	s.Event("worker_ended", map[string]any{"phase": w.phase, "why": e.Why, "suspended": true})
}

// holdAgent sets w's phase aside for the human, who stopped its agent from the agent panel: a message to
// it is refused as stopped by the user, and Claude Code says to start a new agent for it only if the user
// asks (spike 32). Its worker is held, its work kept in its worktree, until `baton run` asks for a fresh agent
// on it; meanwhile its loop goes on watching its jobs, and nothing is sent to it.
func (c *Coordinator) holdAgent(s *state.Store, b *bridge.Bridge, w *worker) {
	now := s.Now()
	w.agent.in = nil
	if agent := c.agentOf(s, w); agent != "" {
		b.Forget(agent)
	}
	c.setEntry(s, w.entry, func(e *state.Worker) {
		if e.Live() {
			e.Status, e.Since, e.Why = state.WorkerHeld, now, "the human stopped its agent"
		}
	})
	s.Event("worker_held", map[string]any{"phase": w.phase, "why": "the human stopped its agent"})
	c.announce(s, decide.AgentHeld(w.phase))
}

// retire ends a worker for good, once its agent has ended (or with none running), for why (RetireWorker):
// its uncommitted work is saved, its jobs are stopped, and its worktree is removed. Its branch goes too,
// unless it holds commits that never landed on the run's branch, which it keeps for them. Its phase, if the
// run still has it working, goes back to the scheduler (state.Unassign).
func (c *Coordinator) retire(now time.Time, s *state.Store, e state.Worker, why string) {
	r := RetireWorker(Retirement{Project: c.Project, Home: c.Home, Run: s, Entry: e, Why: why, By: s.Instance, Host: true, Now: now,
		Snapshot: c.Lead.Snapshot, Logf: c.Lead.Logf, between: c.retireStep})
	if r.Skipped != "" {
		c.Lead.logf("loop: %s: not retired: %s", e.Phase, r.Skipped)
		return
	}
	line := fmt.Sprintf("baton: the phase agent on %s ended: %s", e.Phase, why)
	if r.Unlanded > 0 {
		line += fmt.Sprintf("; its branch %s keeps %d %s that never landed", r.Branch, r.Unlanded, plural(r.Unlanded, "commit", "commits"))
	}
	if r.Kept != "" {
		line += fmt.Sprintf("; its worktree %s was kept, as %s", e.Worktree, r.Kept)
	}
	c.announce(s, line)
}

// Ended suspends the run's phase agents as the host ends: they end with the lead's claude. stop (nil: none)
// runs on each one's sub-run first, while this host still holds it (the host stops its jobs there, as it
// does the lead's); then nothing holds it any more, and the next host for the run starts an agent for it
// again, in its worktree, at once. It returns their workers.
func (c *Coordinator) Ended(why string, stop func(sub *state.Store)) []state.Worker {
	c.fleetWG.Wait()
	s := c.Lead.Store
	var out []state.Worker
	for _, w := range c.launched() {
		w.agent.Ended() // saves its uncommitted work
		c.unregister(w)
		if stop != nil {
			stop(w.store)
		}
		w.store.Update(func(st *state.State) error { state.Release(st, w.store.Instance); return nil })
		if s == nil {
			continue
		}
		now := s.Now()
		c.setEntry(s, w.entry, func(e *state.Worker) {
			if e.Status == state.WorkerStarting || e.Status == state.WorkerWorking || e.Status == state.WorkerLanding {
				e.Status, e.Since, e.Why, e.Ended, e.Merge = state.WorkerSuspended, now, why, now, nil
			}
		})
		s.Event("worker_ended", map[string]any{"phase": w.phase, "why": why, "suspended": true})
		out = append(out, w.entry)
	}
	return out
}

// setEntry changes the roster's entry of worker e, if it is still there.
func (c *Coordinator) setEntry(s *state.Store, e state.Worker, fn func(*state.Worker)) {
	s.Update(func(st *state.State) error {
		if cur := st.Entry(e); cur != nil {
			fn(cur)
		}
		return nil
	})
}

// announce leaves a line for the human in the lead's session (Runtime.Announce).
func (c *Coordinator) announce(s *state.Store, line string) {
	s.Update(func(st *state.State) error { st.Run.Announce = append(st.Run.Announce, line); return nil })
}

// launched lists the phase agents this host started.
func (c *Coordinator) launched() []*worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*worker
	for _, w := range c.workers {
		out = append(out, w)
	}
	return out
}

// runs reports a phase agent of this host working for the worker ref.
func (c *Coordinator) runs(ref string) bool {
	for _, w := range c.launched() {
		if w.entry.Ref == ref {
			return true
		}
	}
	return false
}

func (c *Coordinator) unregister(w *worker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workers[w.phase] == w {
		delete(c.workers, w.phase)
	}
}

// hook calls fn, a test's hook, at step, if there is one.
func hook(fn func(string), step string) {
	if fn != nil {
		fn(step)
	}
}

func phaseName(pl plan.Plan, id string) string {
	if i := pl.Index(id); i >= 0 {
		return id + " (" + pl.Phases[i].Title + ")"
	}
	return id
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return path != "" && err == nil && fi.IsDir()
}
