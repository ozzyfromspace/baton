package loop

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Lanes are the project's lanes, which every session of the run shares: their jobs run in them.
type Lanes interface {
	// Sweep removes the project's lanes that no job holds and no live host brought up.
	Sweep()
	// Down brings down the lanes this host brought up that no job holds, and returns them.
	Down() []int
}

// Coordinator drives a run from its host: a Loop for each of the run's sessions, and what belongs to the
// run as a whole. Each Loop is ticked with its own session's View and injector: the lead's on the lead's
// tick (Tick), and each worker's on its hidden session's (Worker), so that one session typing never holds
// up another. A panic in one session's tick leaves the others ticking. After the lead's Loop, the
// coordinator does the run's own duties: the lanes every session shares are swept once, and brought down
// once the run's plan is complete (a worker's sub-run completing is only its phase done), and the run's
// workers are started, watched and ended (fleet.go). With no workers it is the lead's Loop alone.
type Coordinator struct {
	Lead  *Loop
	Lanes Lanes  // the project's lanes (nil: none)
	Fleet *Fleet // how the run's workers start (nil: none)

	mu      sync.Mutex
	workers map[string]*worker // by phase

	leadPanic, runPanic time.Time // when a panic in the lead's tick, or in the run's duties, was last logged
	swept               bool      // the lanes were swept
	downed              bool      // the lanes came down, as the run's plan is complete

	since     time.Time      // when the host first looked at the workers
	fleetAt   time.Time      // when the last step of work on the workers started
	fleetBusy atomic.Bool    // a step is under way
	fleetWG   sync.WaitGroup // and Ended waits for it
	// Only a step touches these: when a panic in one was last logged, whether the host is ending, why no
	// worker starts in this host any more, how many workers in a row did not start, and when one may start
	// again after a failure.
	fleetPanic   time.Time
	closing      bool
	off          string
	failedStarts int
	retryAt      time.Time
}

// Tick is the lead's tick: its Loop, then the run's duties.
func (c *Coordinator) Tick(v host.View, in host.Injector) {
	guarded(c.Lead, v, "lead", &c.leadPanic, func() { c.Lead.Tick(v, in) })
	guarded(c.Lead, v, "run", &c.runPanic, func() { c.duties(v) })
}

// duties are the run's own, after the lead's Loop: they read the run's state once per tick.
func (c *Coordinator) duties(v host.View) {
	if c.Lanes != nil && !c.swept {
		c.swept = true
		go c.Lanes.Sweep()
	}
	s := c.Lead.Store
	if s == nil || !v.Owner || c.Lanes == nil && c.Fleet == nil {
		return
	}
	st, err := s.Load()
	if err != nil || st.Owner != nil && !st.IsOwner(s.Instance, v.Now) {
		return
	}
	c.lanes(s, st)
	c.fleet(v, s)
}

// Worker makes the Loop of the worker on phase, and returns the controller its hidden session ticks it
// through (host.Spec.Controller). The Loop drives the sub-run its session is bound to, with the lead's
// settings; it never leaves, switches or restarts (RoleWorker).
func (c *Coordinator) Worker(phase string) host.Controller { return c.worker(phase) }

func (c *Coordinator) worker(phase string) *worker {
	lead := c.Lead
	w := &worker{phase: phase, loop: &Loop{Role: RoleWorker, Notify: lead.Notify, Project: lead.Project, Timing: lead.Timing,
		Snapshot: lead.Snapshot, Version: lead.Version, Runners: lead.Runners,
		Logf: func(format string, a ...any) { lead.logf(phase+": "+format, a...) }}}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workers == nil {
		c.workers = map[string]*worker{}
	}
	c.workers[phase] = w
	return w
}

// worker is a worker's Loop, as its session ticks it, and the session the coordinator started it in.
type worker struct {
	phase    string
	loop     *Loop
	panicked time.Time // when its last panic was logged

	// sess is its session (nil until the coordinator starts one), started as entry, whose sub-run is
	// store, at launched. ending is why baton is ending it ("" while it is not), and quick says nothing
	// may be typed into it to end it: it never started, and whatever is on its screen would take the keys.
	sess     Hidden
	entry    state.Worker
	store    *state.Store
	launched time.Time
	ending   string
	quick    bool
}

func (w *worker) Tick(v host.View, in host.Injector) {
	guarded(w.loop, v, w.phase, &w.panicked, func() { w.loop.Tick(v, in) })
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
