package loop

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/risk"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Lanes are the project's lanes, which every session of the run shares: their jobs run in them.
type Lanes interface {
	// Sweep removes the project's lanes that no job holds and no live host brought up.
	Sweep()
	// Down brings down the lanes this host brought up that no job holds, and returns them.
	Down() []int
}

// Coordinator drives a run from its host: the lead's Loop, ticked with the lead's View and injector on the
// lead's tick (Tick), and what belongs to the run as a whole. After the lead's Loop, the coordinator does
// the run's own duties: the lanes every session shares are swept once, and brought down once the run's plan
// is complete (a worker's sub-run completing is only its phase done), the run's phase agents are started,
// watched and ended (fleet.go), each with a loop of its own (Agent), a run that stops moving is told to
// the human (stall.go), and so is a plan ready for their review before its QA phases end (review.go). A
// panic in one part of the tick leaves the others going. With no workers it is the lead's Loop alone, the
// stall alarm and the review notice.
type Coordinator struct {
	Lead  *Loop
	Lanes Lanes  // the project's lanes (nil: none)
	Fleet *Fleet // how the run's workers start (nil: none)
	// Project is the run's project, and Home baton's home, which keeps the workers' worktrees. A host with
	// no Fleet still retires the workers its run has on record (fleet.go).
	Project *state.Project
	Home    string
	// Leftovers reads what phases left running, and stops it (zero: reads nothing): retiring a worker stops
	// what it left in its worktree, and a gate slow on a busy machine is told what finished phases left
	// (leftover.go). leftAt is when a step last read the machine for that.
	Leftovers leftover.Finder
	leftAt    time.Time
	// Advisor is the advisor's settings (advisor.go): a small model's note on each look-in at a gate.
	Advisor Advisor
	// WeeklyReserve is weekly_reserve_pct: the percent of the account's weekly allowance the run leaves,
	// at which it winds down until the weekly reset (winddown.go; 0: no wind-down).
	WeeklyReserve float64

	mu      sync.Mutex
	workers map[string]*worker // by phase: the phase agents this host started

	leadPanic, runPanic time.Time // when a panic in the lead's tick, or in the run's duties, was last logged
	swept               bool      // the lanes were swept
	downed              bool      // the lanes came down, as the run's plan is complete

	since     time.Time      // when the host first looked at the workers
	fleetAt   time.Time      // when the last step of work on the workers started
	fleetBusy atomic.Bool    // a step is under way
	fleetWG   sync.WaitGroup // and Ended waits for it
	// Only a step touches these: when a panic in one was last logged, whether the host is ending, why no
	// worker starts in this host any more, when one may start again after a failure, and whether this host
	// has claimed the run's bridge (the commands an earlier host left are void).
	fleetPanic time.Time
	closing    bool
	off        string
	retryAt    time.Time
	claimed    bool
	landAt     map[string]time.Time // by a worker's sub-run: when the merge queue tries to land it again
	// The prompts for phases' agents the lead's input box is to be emptied of, and those kept for it
	// already, by when they were written (words.go).
	empties []toEmpty
	emptied map[int64]bool
	// Whether baton's mod has said hello in the lead's claude, and since when the host has waited for it
	// while a plan runs (mod.go), for the run a step last looked at (stepRun: claimed, off and these are
	// that run's). Only a step touches them.
	modReady bool
	modSince time.Time
	stepRun  string
	// The machine's swap readings of the last minute, and why it had no room for another worker at the
	// last step ("" if it had); the kind of room it lacks ("" for none) and since when, and the kind the
	// log last said (room.go).
	swaps     []swapAt
	noRoom    string
	roomKind  string
	roomSince time.Time
	roomSaid  string
	// genRun and gen are the host's run and its plan's generation as the lead's tick last saw them; a
	// plan attached since owes a sweep of the workers other runs left behind (sweep.go). Only the lead's
	// tick touches them.
	genRun    string
	gen       int
	sweepOwed bool
	isBaton   func(pid int) bool // tells a running baton by its pid (nil: batonRunning; tests)
	// AbandonAfter is how long another run must have been quiet before its workers count as abandoned
	// (0: state.AbandonAfter; tests shorten it).
	AbandonAfter time.Duration
	// Risk is the plan's lines for its risk ledger (config.Risk; off unless set), and rsk the ledger step's
	// own state (risk.go).
	Risk risk.Lines
	rsk  ledger

	// retireStep and resumeStep are called after each step of retiring and resuming a worker (tests).
	retireStep, resumeStep func(step string)

	// stallAt is when the lead's tick last looked at whether the run stopped moving, and logs the running
	// jobs' logs as it saw them (stall.go). Only the lead's tick touches them.
	stallAt time.Time
	logs    map[string]logSeen
	// lookAt is when the lead's tick last looked for look-ins at the run's gates and landings (lookin.go).
	lookAt time.Time
	// reviewAt is when the lead's tick last looked at whether the plan is ready for the human's review
	// (review.go).
	reviewAt time.Time

	rec recorder // the record step's (record.go)
	adv advising // the advisor's calls, in memory (advisor.go)

	// run is the account's usage, its limit and its resume timer, and paused whether the run is paused, as the
	// last step of usage found them, for the agents' Loops (usage.go); usageAt is when that step was.
	run     atomic.Pointer[state.Usage]
	paused  atomic.Bool
	usageAt time.Time
	// pending says a newer baton waits for the lead's restart, which ends the phase agents: no new one
	// starts. stopping says the lead's claude was asked to end (a restart, or the human leaving baton): the
	// host ends, and so do the phase agents in it, which Ended suspends.
	pending, stopping atomic.Bool
}

// Tick is the lead's tick: its Loop, then the run's duties.
func (c *Coordinator) Tick(v host.View, in host.Injector) {
	c.Lead.Workers = c.Fleet != nil
	c.Lead.advise = c.advise // the lead's look-ins, before its loop delivers them (advisor.go)
	guarded(c.Lead, v, "lead", &c.leadPanic, func() { c.Lead.Tick(v, in) })
	if c.Lead.quit {
		c.stopping.Store(true)
	}
	guarded(c.Lead, v, "run", &c.runPanic, func() { c.duties(v) })
}

// duties are the run's own, after the lead's Loop: they read the run's state once per tick.
func (c *Coordinator) duties(v host.View) {
	if c.Lanes != nil && !c.swept {
		c.swept = true
		go c.Lanes.Sweep()
	}
	s := c.Lead.Store
	if s == nil || !v.Owner || c.Lanes == nil && c.Fleet == nil && c.Project == nil {
		return
	}
	st, err := s.Load()
	if err != nil || st.Owner != nil && !st.IsOwner(s.Instance, v.Now) {
		return
	}
	c.pending.Store(c.Fleet != nil && c.Lead.upgradeHolds(v, st))
	c.records(v, s, st)
	c.lanes(s, st)
	c.fleetOn(s, st) // the run's QA phases go to its agents (review.go)
	// With workers or none, a run holds on the account's usage limit (usage.go).
	if st.Mode != state.ModeIdle && v.Now.Sub(c.usageAt) >= fleetEvery {
		c.usageAt = v.Now
		c.usage(v.Now, s, st)
	}
	c.fleet(v, s, st)
	c.stalls(v, s, st)
	c.reviewReady(v.Now, s, st)
	c.lookIns(v, s, st)
	c.riskLedger(v, s, st)
}

// worker is a phase agent this host asked baton's mod to start in the lead's claude: its roster entry, its
// sub-run, and its loop (Agent). spawn is the bridge's command starting it until the mod has acked it, issued
// at launched; ending is why baton is ending it ("" while it is not), since endAt, and stop the command
// stopping it, until acked. sending is the message to the agent not acked yet, sent at sentAt (line.go).
type worker struct {
	phase    string
	entry    state.Worker
	store    *state.Store
	agent    *Agent
	launched time.Time
	spawn    int
	ending   string
	endAt    time.Time
	stop     int
	sending  int
	sentAt   time.Time
	// words names the human's words (state.HumanWords.At) the message wordsCmd gives its agent, until
	// the mod has acked it (words.go).
	words    time.Time
	wordsCmd int
	session  string // the lead's session its agent lives in: a /clear of the lead's ends it
	stray    string // its agent, started in a plain worktree baton made for it: that goes once it has ended
	// erred is when the API error a fresh agent avoids, last found to end its agent's turn, ended it
	// (afresh.go).
	erred time.Time
}

// guarded runs fn, a part of a session's tick, so that a panic in it is logged, in l's log and at most
// once a minute (*last) in the session's run, and goes no further: a bug in one session's tick must not
// stop the run's other sessions, or the rest of the tick.
func guarded(l *Loop, v host.View, what string, last *time.Time, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		l.logf("loop: %s: panic: %v", what, r)
		s := v.Store
		if s == nil {
			s = l.Store
		}
		if s != nil && v.Now.Sub(*last) > time.Minute {
			*last = v.Now
			s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": what})
		}
	}()
	fn()
}

// lanes brings down the lanes this host brought up once the run's plan is complete: nothing runs in them
// until the next plan, and what they hold up (services, containers) would otherwise stay up until the
// session ends. A complete plan has no job left running. (The lanes hosts that are gone left behind are
// swept up once, on the first tick.)
func (c *Coordinator) lanes(s *state.Store, st state.State) {
	if c.Lanes == nil {
		return
	}
	if st.Mode != state.ModeComplete {
		c.downed = false
	} else if !c.downed {
		c.downed = true
		go c.lanesDown(s)
	}
}

// lanesDown brings down the lanes this host brought up, as the run's plan is complete.
func (c *Coordinator) lanesDown(s *state.Store) {
	if lanes := c.Lanes.Down(); len(lanes) > 0 {
		c.Lead.logf("loop: the plan is complete: brought lanes %v down", lanes)
		s.Event("lanes_down", map[string]any{"lanes": lanes, "why": "plan complete"})
	}
}
