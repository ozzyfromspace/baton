package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The coordinator runs the run's workers: hidden sessions that each work on a phase that can go ahead
// beside the lead's, in a git worktree of its own, on a branch of its own made from the run's branch
// (docs/research/workers.md). It starts one whenever a slot is free, a phase can start, and the machine
// has room for another session (Fleet.next, room.go).
// Before claude starts, it writes the worker onto the run's roster and links its sub-run to its session
// and instance, so the worker's hooks find their run and never bind one. From then on it watches the
// worker: its session must send its first prompt within Timing.WorkerStart, and an unexpected exit is
// resumed in its worktree, MaxResumes times at most. A worker the run no longer counts on (its run
// dropped, its plan replaced, its phase gone from the plan) is ended, and retired (RetireWorker): its
// uncommitted work is saved and its worktree removed. Its branch goes too, unless it holds commits that
// never landed. A worker whose session ended with the host is suspended (Ended), and the next host for
// the run resumes it in its worktree, or retires it when that host runs no workers. All of this runs off
// the lead's tick, one step at a time, since git and claude take a while to start.

// MaxResumes is how many times a worker's session is resumed after it ended unexpectedly. After that its
// phase goes back to the scheduler, and its commits stay on its branch.
const MaxResumes = 2

// fleetEvery is how often the coordinator looks over the run's workers.
const fleetEvery = time.Second

// gitTimeout bounds each git command run for a worker: making its worktree checks the project out.
const gitTimeout = 2 * time.Minute

// launchRetry is how long the coordinator waits before it starts a worker again after it failed to
// start one (no worktree, no session).
const launchRetry = 5 * time.Minute

// Hidden is a worker's session, as the coordinator sees it (host.Session).
type Hidden interface {
	Done() <-chan struct{}
	ExitCode() int
	End() error
	Quit() error
	Tail() []byte
}

// Launch is how the host starts claude. A worker's session is started the same way as the lead's, with
// the lead's own flags, in its worktree and with an environment of its own.
type Launch struct {
	Args        []string // the lead's flags a worker keeps (elevate.KeepUserArgs): its model, permissions, plugins
	Autocompact string   // passed as --autocompact ("" for none)
	Settings    string   // baton's hooks and status line, passed as --settings (host.SettingsJSON)
	Env         []string // the lead's base environment: any BATON_* in it, and its working directory, are dropped
	BatonEnv    []string // baton's settings for the sessions it hosts: the valves, escalation, BATON_HOME
	Bin         string   // the baton binary the hooks call
	Version     string
	// ScreenDump keeps the latest of each worker session's screen in its sub-run (screen.log) as it ends
	// (BATON_SCREEN_DUMP=1), for debugging only.
	ScreenDump bool
}

// args is claude's command line for a worker's session: started anew under session, or resumed. The
// prompt comes after "--", since a variadic flag such as --allowedTools would take it as one of its own
// (spike 23).
func (l Launch) args(session string, resume bool, prompt string) []string {
	var args []string
	if l.Autocompact != "" {
		args = append(args, "--autocompact", l.Autocompact)
	}
	args = append(append(args, "--settings", l.Settings), l.Args...)
	if resume {
		args = append(args, "--resume", session)
	} else {
		args = append(args, "--session-id", session)
	}
	return append(args, "--", prompt)
}

// env is the environment of worker e's session, a worker of run in the project whose .baton is dir and
// whose checkout is checkout. Nothing in it names the lead's session or terminal (BATON_TTY,
// BATON_PENDING, BATON_RECOVER), and it has no PWD: claude would take an inherited one for its working
// directory, the checkout's, instead of the worktree's.
func (l Launch) env(e state.Worker, run, dir, checkout string) []string {
	var env []string
	for _, kv := range l.Env {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(k, "BATON_") && k != "PWD" && k != "OLDPWD" {
			env = append(env, kv)
		}
	}
	for _, kv := range l.BatonEnv {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "BATON_TTY", "BATON_PENDING", "BATON_RECOVER", "BATON_HOST", "BATON_INSTANCE", "BATON_BIN", "BATON_VERSION", "BATON_DIR",
			"BATON_ROLE", "BATON_PHASE", "BATON_RUN", "BATON_WORKTREE", "BATON_CHECKOUT":
		default:
			env = append(env, kv)
		}
	}
	return append(env, "BATON_HOST=1", "BATON_INSTANCE="+e.Instance, "BATON_BIN="+l.Bin, "BATON_VERSION="+l.Version, "BATON_DIR="+dir,
		"BATON_ROLE=worker", "BATON_PHASE="+e.Phase, "BATON_RUN="+run, "BATON_WORKTREE="+e.Worktree, "BATON_CHECKOUT="+checkout)
}

// Fleet is what the coordinator needs to run workers. A coordinator without one runs none (workers: 0, or
// a machine with no room for one), and retires those its run has on record.
type Fleet struct {
	Max int // workers at once: workers' number, or with auto what the machine allows (config.AutoWorkers)
	// Auto is workers: auto: new workers start one Ramp apart (0: DefaultRamp).
	Auto     bool
	Ramp     time.Duration
	UsageMax float64 // no new worker above this percent of the five-hour allowance (worker_usage_max; 0: no check)
	MaxAuto  int     // the decisions a phase may make without the human before they review them (max_auto_decisions; 0: no cap)
	// Machine reads the machine (nil: machine.Read): no worker starts or resumes while it has no room for
	// another session (room.go).
	Machine func() machine.Reading
	Launch  Launch
	// Spawn starts a hidden session (host.Supervisor.Spawn). It fails with host.ErrClosed once the host
	// is ending.
	Spawn func(host.Spec) (Hidden, error)
	// Go runs the coordinator's work on the workers off the lead's tick (nil: on a goroutine of its own).
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
// HoldLanes, HoldConflicts, HoldUsage); "" for none. With Auto, new workers start a Ramp apart, so that
// the machine's readings show each one before the next starts: that wait is no hold.
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
	case f.Auto && now.Sub(r.started) < f.ramp():
		return phase, fmt.Sprintf("a worker started %s ago, and with workers: auto the next starts %s after it", decide.Span(now.Sub(r.started)), decide.Span(f.ramp())), ""
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

// rosterEntry is the roster's entry of the worker on ref started as session, or nil.
func rosterEntry(st *state.State, ref, session string) *state.Worker {
	for i := len(st.Workers) - 1; i >= 0; i-- {
		if w := &st.Workers[i]; w.Ref == ref && w.Session == session {
			return w
		}
	}
	return nil
}

// fleet starts a step of the coordinator's work on the workers, unless one is still going, at most once a
// second. A host with no Fleet steps only while its run st has workers on record, to retire them, or
// once a plan attached owes a sweep of those other runs left behind; otherwise it does nothing at all.
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

// step looks over the run's workers once: first, when a plan was attached (sweep), the workers other runs
// left behind; then the sessions this host runs, the merge queue and the answers to their questions, the
// roster's workers no session runs, and whether a new one can start. With no Fleet, it only retires the
// roster's workers: workers are off in this host, and their phases go back to the lead.
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
	c.usage(now, s, st)
	c.look(now)
	for _, w := range c.launched() {
		c.watch(now, s, st, pl, w)
		c.waiting(now, s, st, w)
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
		} else if c.mayLaunch(now, s, st) == "" && c.noRoom == "" {
			why := e.Why
			if why == "" {
				why = "it ended with the baton session that ran it"
			}
			c.resume(now, s, e, why, false)
		}
	}
	if st, err = s.Load(); err != nil {
		return
	}
	phase, why, held := "", c.mayLaunch(now, s, st), ""
	if why == "" {
		phase, why, held = c.Fleet.next(st, pl, now, room{busy: c.noRoom, lanes: lanes(now, s, st), started: c.started})
	}
	c.hold(now, s, st, phase, why, held)
	if phase != "" && why == "" {
		c.spawn(now, s, st, pl, phase)
	}
}

// mayLaunch says why this host may start no worker's session now, or "" if it may, room on the machine
// aside (Coordinator.noRoom). The lead's session must have started first: until a hook of its own has
// run, it may be showing Claude Code's trust dialog, behind which nothing runs (spike 23), and a worker
// started then would meet the same dialog.
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
		return "a worker failed to start a moment ago"
	case !gitx.Usable(s.Checkout):
		return "the project is not a git repository"
	}
	if why := c.modWait(now, s, st); why != "" {
		return why
	}
	return launchable(st, now)
}

// watch looks at a worker whose session this host started: whether it has ended, whether the run still
// counts on it, and whether it has started.
func (c *Coordinator) watch(now time.Time, s *state.Store, st state.State, pl plan.Plan, w *worker) {
	e := rosterEntry(&st, w.entry.Ref, w.entry.Session)
	select {
	case <-w.sess.Done():
		if !c.stopping.Load() { // or it ended with the host, and Ended suspends it
			c.exited(now, s, w, e, unwanted(st, pl, e))
		}
		return
	default:
	}
	switch why := unwanted(st, pl, e); {
	case w.ending != "":
	case why != "":
		c.end(w, why)
	case e.Status == state.WorkerStarting:
		c.starting(now, s, w)
	}
}

// starting waits for a worker's session to send its first prompt, which is what started means. One that
// has not within Timing.WorkerStart never will: it is ended, and a trust screen stops workers in this
// host, since baton never answers one (its default is to exit).
func (c *Coordinator) starting(now time.Time, s *state.Store, w *worker) {
	if sub, err := w.store.Load(); err == nil && !sub.Run.TurnStarted.Before(w.launched) {
		c.failedStarts = 0
		c.setEntry(s, w.entry, func(e *state.Worker) {
			e.Status, e.Since = state.WorkerWorking, now
			if e.Started.IsZero() {
				e.Started = now
			}
		})
		s.Event("worker_started", map[string]any{"phase": w.phase, "session": w.entry.Session, "after": now.Sub(w.launched).Round(time.Second).String()})
		c.announce(s, fmt.Sprintf("baton: the worker on %s is at work", w.phase))
		return
	}
	if now.Sub(w.launched) < c.Lead.Timing.WorkerStart {
		return
	}
	w.quick = true // nothing is typed into a session that never started: whatever is on its screen would take it
	why := fmt.Sprintf("its session did not start within %s", c.Lead.Timing.WorkerStart)
	if trustScreen(w.sess.Tail()) {
		why = "Claude Code asked whether to trust its worktree"
		c.stopLaunching(now, s, "Claude Code asked whether to trust a worker's worktree, which baton never answers. Trust the project in Claude Code, then restart the session")
	}
	c.end(w, why)
}

// waiting keeps the roster's word of what in worker w's own session waits on the human (Worker.Needs),
// which the lead's status line and baton status show. Nobody watches a worker's session, so the human is
// told in the lead's, once for each thing, and where to answer it. (A question of its that they chose to
// answer in its session was announced as they chose it.)
func (c *Coordinator) waiting(now time.Time, s *state.Store, st state.State, w *worker) {
	select {
	case <-w.sess.Done():
		return
	default:
	}
	e := rosterEntry(&st, w.entry.Ref, w.entry.Session)
	if e == nil || !e.Live() || w.ending != "" {
		return
	}
	sub, err := w.store.Load()
	if err != nil {
		return
	}
	what, relayed := needs(sub, st, w.phase)
	if what == e.Needs {
		return
	}
	c.setEntry(s, *e, func(e *state.Worker) {
		e.Needs, e.NeedsSince = what, now
		if what == "" {
			e.NeedsSince = time.Time{}
		}
	})
	if what == "" || relayed {
		return
	}
	s.Event("worker_waiting", map[string]any{"phase": w.phase, "what": what})
	c.announce(s, fmt.Sprintf("baton: the worker on %s waits for you: %s. Answer it in its session: baton view %s, in another terminal", w.phase, strings.TrimSuffix(what, "."), w.phase))
}

// needs says what in worker phase's own session waits on the human, from its sub-run sub and the run's
// state st: the dialog Claude Code shows there, an escalation of its watchdog, or its question the human
// chose to answer there (relayed). "" for nothing. A question the lead puts to the human is not the
// worker's session's to show.
func needs(sub, st state.State, phase string) (what string, relayed bool) {
	if d, ok := sub.Run.Dialogs.Front(); ok {
		if d.Tool == "AskUserQuestion" {
			return "a question on its screen", false
		}
		return "a permission prompt for " + d.Tool, false
	}
	if e := sub.Run.Escalation; e != nil && e.Watchdog {
		return e.Reason, false
	}
	for _, r := range slices.Backward(st.Relay) {
		if r.Phase != phase || r.Answered == nil || r.Answered.By == state.ByWithdrawn || !sub.Waits(r) {
			continue
		}
		if o := decide.RelayOutcome(r, r.Answered.Answer); o == decide.OutcomeWait || o == decide.OutcomePause {
			switch r.Kind {
			case state.RelayProposal:
				return "its proposal " + r.Decision + ", " + r.What, true
			case state.RelayReview:
				return "a review of the decisions it made without you", true
			}
			return "its block, " + r.Because, true
		}
	}
	return "", false
}

// trustRE finds Claude Code's question whether to trust a folder on a screen, its words run together.
var (
	escapeRE = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-_])`)
	trustRE  = regexp.MustCompile(`(?i)trustthisfolder|doyoutrust`)
)

// trustScreen reports Claude Code's trust dialog on a session's screen.
func trustScreen(tail []byte) bool {
	text := escapeRE.ReplaceAllString(string(tail), "")
	return trustRE.MatchString(strings.Join(strings.Fields(text), ""))
}

// end ends a worker's session, for why. Once it has, the worker is retired (exited).
func (c *Coordinator) end(w *worker, why string) {
	w.ending = why
	c.Lead.logf("loop: %s: ending its worker: %s", w.phase, why)
	if w.quick {
		w.sess.Quit()
	} else {
		w.sess.End()
	}
}

// exited deals with a worker whose session has ended: one baton ended is retired, and so is one the run
// no longer counts on (unwanted says why); one that ended by itself is resumed, unless it has been too
// often already.
func (c *Coordinator) exited(now time.Time, s *state.Store, w *worker, e *state.Worker, unwanted string) {
	code := w.sess.ExitCode()
	c.forget(w)
	entry := w.entry
	if e != nil {
		entry = *e
	}
	switch {
	case w.ending != "":
		c.retire(now, s, entry, w.ending)
		if w.quick {
			c.startFailed(now, s, w.phase, w.ending)
		}
	case unwanted != "":
		c.retire(now, s, entry, unwanted)
	case e.Resumes >= MaxResumes:
		why := fmt.Sprintf("its session ended unexpectedly (exit code %d), %d times", code, e.Resumes+1)
		c.retire(now, s, entry, why)
		c.later(now, s, w.phase, why)
	default:
		c.resume(now, s, entry, fmt.Sprintf("it ended unexpectedly (exit code %d)", code), true)
	}
}

// startFailed counts a worker whose session never started (why). Two in a row stop workers in this host.
func (c *Coordinator) startFailed(now time.Time, s *state.Store, phase, why string) {
	if c.failedStarts++; c.failedStarts >= 2 {
		c.stopLaunching(now, s, "two workers in a row did not start; the log (.baton/baton.log) says what happened")
		return
	}
	c.later(now, s, phase, why)
}

// stopLaunching starts no more workers in this host, for why, and tells the human.
func (c *Coordinator) stopLaunching(now time.Time, s *state.Store, why string) {
	if c.off != "" {
		return
	}
	c.off = why
	text := "no more workers start in this session: " + why
	s.Update(func(st *state.State) error {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "workers", Text: text, At: now})
		st.Run.Announce = append(st.Run.Announce, "baton: "+text)
		return nil
	})
	s.Event("workers_off", map[string]any{"why": why})
}

// spawn starts a worker on phase. The roster has it first, so that nothing else takes the phase; then its
// worktree and its sub-run are made, linked to the session id baton picks, and only then does claude start.
func (c *Coordinator) spawn(now time.Time, s *state.Store, st state.State, pl plan.Plan, phase string) {
	f := c.Fleet
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
	e := state.Worker{Phase: phase, Ref: state.WorkerRef(s.Ref, phase), Session: state.NewSessionID(), Instance: state.WorkerInstance(s.Instance, phase),
		Worktree: dir, Branch: "baton/" + s.Ref + "/" + phase, Base: base}
	if _, err := s.Update(func(st *state.State) error { return state.Assign(st, pl, e, now) }); err != nil {
		c.Lead.logf("loop: %s: no worker: %v", phase, err) // the lead took it up meanwhile
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if _, err := gitx.AddBranch(ctx, s.Checkout, dir, e.Branch, base); err != nil {
		c.retire(now, s, e, "baton could not make its worktree: "+err.Error())
		c.later(now, s, phase, err.Error())
		return
	}
	sub, err := c.subRun(s, pl, e)
	if err != nil {
		c.retire(now, s, e, "baton could not make its run: "+err.Error())
		c.later(now, s, phase, err.Error())
		return
	}
	if _, err := c.launch(now, s, e, sub, f.Launch.args(e.Session, false, decide.WorkerBegin(phase))); err != nil {
		c.unlaunched(now, s, e, err)
		return
	}
	c.started = now
	s.Event("worker_spawned", map[string]any{"phase": phase, "session": e.Session, "worktree": dir, "branch": e.Branch, "base": base})
	c.announce(s, fmt.Sprintf("baton: a worker took up %s, in a worktree of its own on the branch %s (baton view %s in another terminal shows it)",
		phaseName(pl, phase), e.Branch, phase))
}

// later puts off starting workers after one failed to start for why.
func (c *Coordinator) later(now time.Time, s *state.Store, phase, why string) {
	c.retryAt = now.Add(launchRetry)
	c.Lead.logf("loop: %s: no worker for now: %s", phase, why)
	s.Event("worker_deferred", map[string]any{"phase": phase, "why": why, "until": c.retryAt.UTC().Format(time.RFC3339)})
	c.announce(s, fmt.Sprintf("baton: no worker on %s for now (%s) → baton tries again at %s", phase, why, c.retryAt.Local().Format("15:04")))
}

// subRun makes worker e's sub-run, linked to its session and instance: the run's plan narrowed to its
// phase, in hand.
func (c *Coordinator) subRun(s *state.Store, pl plan.Plan, e state.Worker) (*state.Store, error) {
	sub, err := c.Project.NewWorker(s.Ref, e.Phase, e.Session, s.Instance, e.Worktree)
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

// launch starts the session of worker e, whose sub-run is sub, with claude's arguments args, ticked by a
// Loop of its own.
func (c *Coordinator) launch(now time.Time, s *state.Store, e state.Worker, sub *state.Store, args []string) (*worker, error) {
	f := c.Fleet
	w := c.worker(e.Phase)
	spec := host.Spec{Name: e.Phase, Dir: e.Worktree, Args: args, Env: f.Launch.env(e, s.Ref, c.Project.Dir, s.Checkout),
		Instance: e.Instance, Controller: w}
	if f.Launch.ScreenDump {
		spec.Dump = filepath.Join(sub.Dir, "screen.log")
	}
	h, err := f.Spawn(spec)
	if err != nil {
		c.unregister(w)
		return nil, err
	}
	c.mu.Lock() // the lead's tick reads them too (restartHold)
	w.sess, w.entry, w.store, w.launched = h, e, sub, now
	c.mu.Unlock()
	c.Lead.logf("loop: %s: started its worker in %s", e.Phase, e.Worktree)
	return w, nil
}

// unlaunched deals with a worker whose session could not start: one that was starting as the host ended
// is suspended, for the next host to start; any other is retired.
func (c *Coordinator) unlaunched(now time.Time, s *state.Store, e state.Worker, err error) {
	if errors.Is(err, host.ErrClosed) {
		c.closing = true
		c.setEntry(s, e, func(w *state.Worker) {
			w.Status, w.Since, w.Why = state.WorkerSuspended, now, "the baton session ended as it started"
		})
		c.Project.Unbind(e.Instance)
		return
	}
	c.retire(now, s, e, "baton could not start its session: "+err.Error())
	c.later(now, s, e.Phase, err.Error())
}

// resume starts worker e's session again in its worktree, for why: the same conversation, or, for a
// session that never started, a new one. unexpected counts it against MaxResumes. Once its sub-run is
// claimed, the worker must still be on the roster as e was read, and not retiring: another baton process
// may be retiring it meanwhile (RetireWorker), and a worker is never both resumed and retired.
func (c *Coordinator) resume(now time.Time, s *state.Store, e state.Worker, why string, unexpected bool) {
	hook(c.resumeStep, "read")
	if !isDir(e.Worktree) {
		c.retire(now, s, e, "its worktree is gone")
		return
	}
	again := !e.Started.IsZero()
	prev := e
	if !again {
		e.Session = state.NewSessionID() // a session id is never used twice (spike 23)
	}
	e.Instance = state.WorkerInstance(s.Instance, e.Phase)
	sub, err := c.Project.NewWorker(s.Ref, e.Phase, e.Session, s.Instance, e.Worktree)
	if err != nil {
		c.Lead.logf("loop: %s: not resumed yet: %v", e.Phase, err) // still claimed by the host that ran it
		return
	}
	hook(c.resumeStep, "claimed")
	if unexpected {
		e.Resumes++
	}
	var held string
	_, err = s.Update(func(st *state.State) error {
		w := rosterEntry(st, prev.Ref, prev.Session)
		switch {
		case w == nil || !w.Live() || w.Status != prev.Status || !w.Since.Equal(prev.Since):
			held = "it changed since it was looked at"
		case w.RetiringBy(s.Instance, now):
			held = w.Retirer + " is retiring it"
		}
		if held != "" {
			return errKept
		}
		w.Session, w.Instance, w.Resumes = e.Session, e.Instance, e.Resumes
		w.Status, w.Since, w.Why, w.Ended, w.Merge = state.WorkerStarting, now, "", now, nil
		w.Needs, w.NeedsSince = "", time.Time{}
		w.Retiring, w.Retirer = time.Time{}, ""
		return nil
	})
	if err != nil && held == "" {
		held = err.Error()
	}
	if held != "" {
		c.Lead.logf("loop: %s: not resumed: %s", e.Phase, held)
		c.Project.Unbind(e.Instance)
		return
	}
	hook(c.resumeStep, "guarded")
	prompt := decide.WorkerBegin(e.Phase)
	if again {
		prompt = decide.WorkerResume(e.Phase, why)
	}
	if _, err := c.launch(now, s, e, sub, c.Fleet.Launch.args(e.Session, again, prompt)); err != nil {
		c.unlaunched(now, s, e, err)
		return
	}
	c.started = now
	s.Event("worker_resumed", map[string]any{"phase": e.Phase, "session": e.Session, "why": why, "resumes": e.Resumes, "anew": !again})
	c.announce(s, fmt.Sprintf("baton: resumed the worker on %s: %s", e.Phase, why))
}

// retire ends a worker for good, once its session has ended (or with none running), for why
// (RetireWorker): its uncommitted work is saved, its jobs are stopped, and its worktree is removed. Its
// branch goes too, unless it holds commits that never landed on the run's branch, which it keeps for
// them. Its phase, if the run still has it working, goes back to the scheduler (state.Unassign).
func (c *Coordinator) retire(now time.Time, s *state.Store, e state.Worker, why string) {
	r := RetireWorker(Retirement{Project: c.Project, Home: c.Home, Run: s, Entry: e, Why: why, By: s.Instance, Host: true, Now: now,
		Snapshot: c.Lead.Snapshot, Logf: c.Lead.Logf, between: c.retireStep})
	if r.Skipped != "" {
		c.Lead.logf("loop: %s: not retired: %s", e.Phase, r.Skipped)
		return
	}
	line := fmt.Sprintf("baton: the worker on %s ended: %s", e.Phase, why)
	if r.Unlanded > 0 {
		line += fmt.Sprintf("; its branch %s keeps %d %s that never landed", r.Branch, r.Unlanded, plural(r.Unlanded, "commit", "commits"))
	}
	if r.Kept != "" {
		line += fmt.Sprintf("; its worktree %s was kept, as %s", e.Worktree, r.Kept)
	}
	c.announce(s, line)
}

// Ended suspends the run's workers as the host ends, their sessions with it: the next host for the run
// resumes each one in its worktree. It returns them, so the host can stop their jobs and release their
// instances as it does the lead's.
func (c *Coordinator) Ended(why string) []state.Worker {
	c.fleetWG.Wait()
	s := c.Lead.Store
	var out []state.Worker
	for _, w := range c.launched() {
		w.loop.Ended() // saves its uncommitted work
		w.loop.Flush()
		c.unregister(w)
		if s == nil {
			continue
		}
		now := s.Now()
		c.setEntry(s, w.entry, func(e *state.Worker) {
			if e.Status == state.WorkerStarting || e.Status == state.WorkerWorking || e.Status == state.WorkerLanding {
				e.Status, e.Since, e.Why, e.Ended, e.Merge = state.WorkerSuspended, now, why, now, nil
				e.Needs, e.NeedsSince = "", time.Time{}
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
		if cur := rosterEntry(st, e.Ref, e.Session); cur != nil {
			fn(cur)
		}
		return nil
	})
}

// announce leaves a line for the human in the lead's session (Runtime.Announce).
func (c *Coordinator) announce(s *state.Store, line string) {
	s.Update(func(st *state.State) error { st.Run.Announce = append(st.Run.Announce, line); return nil })
}

// launched lists the workers whose sessions this host started.
func (c *Coordinator) launched() []*worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*worker
	for _, w := range c.workers {
		if w.sess != nil {
			out = append(out, w)
		}
	}
	return out
}

// runs reports a session of this host running the worker ref.
func (c *Coordinator) runs(ref string) bool {
	for _, w := range c.launched() {
		if w.entry.Ref == ref {
			return true
		}
	}
	return false
}

// forget lets go of a worker whose session has ended: its Loop saves its uncommitted work, and sends what
// it has queued.
func (c *Coordinator) forget(w *worker) {
	c.unregister(w)
	w.loop.Ended()
	w.loop.Flush()
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
