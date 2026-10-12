package loop

import (
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Agent is the loop of one phase agent this host started, which the coordinator ticks at each step of its
// work on the workers. A phase agent has no session of its own to tick a Loop: its hooks, routed to its
// sub-run, keep what its conversation does (its turns, its activity, its stops), and Agent does the rest,
// with the lead's own code: it keeps the sub-run's claim fresh (the owner's heartbeat, which tells every
// other baton process that the worker is at work), runs the sub-run's jobs and settles them, saves the
// worktree's uncommitted work when the sub-run halts, and sends the human the sub-run's notices. Once the
// agent is at work, its watchdog is the lead's (Loop.watch), reaching it through its line (line.go) as the
// lead's reaches the lead through the keyboard: an idle agent is woken for its results and baton's
// messages, reminded when it stops with its phase unreported, and its turn retried after an API error; a
// refused turn is escalated, never retried.
type Agent struct {
	Phase string
	Store *state.Store
	loop  *Loop // the sub-run's jobs, their settling, the snapshot of a halt and the watchdog, as a Loop does them
	beat  time.Time
	panic time.Time     // when a panic in its tick was last logged
	in    host.Injector // its agent's line, once the agent is at work, until baton ends it; nil otherwise
}

// newAgent is the loop of the agent on phase, whose sub-run is sub, with the lead's settings.
func (c *Coordinator) newAgent(phase string, sub *state.Store) *Agent {
	lead := c.Lead
	return &Agent{Phase: phase, Store: sub, loop: &Loop{Store: sub, Notify: lead.Notify, Project: lead.Project + " · " + phase,
		Timing: lead.Timing, Snapshot: lead.Snapshot, Version: lead.Version, Runners: lead.Runners, RunLimited: c.runLimited,
		Logf: func(format string, a ...any) { lead.logf(phase+": "+format, a...) }, owner: true}}
}

// beatEvery is how often an agent's sub-run's claim is refreshed: as often as a session's host refreshes
// its own.
const beatEvery = state.OwnerTTL / 6

// Tick runs the agent's loop once, at now. A panic in it is logged in the sub-run, and leaves the rest of
// the coordinator's step going.
func (a *Agent) Tick(now time.Time) {
	guarded(a.loop, host.View{Now: now, Store: a.Store}, a.Phase, &a.panic, func() { a.tick(now) })
}

func (a *Agent) tick(now time.Time) {
	if a.beat.IsZero() || now.Sub(a.beat) >= beatEvery {
		a.beat = now
		if _, err := a.Store.Update(func(st *state.State) error { return state.Claim(st, a.Store.Instance, os.Getpid(), now) }); err != nil {
			a.loop.logf("loop: heartbeat: %v", err)
		}
	}
	st, err := a.Store.Load()
	if err != nil {
		a.loop.logf("loop: %v", err)
		return
	}
	v := host.View{Now: now, Store: a.Store, Owner: true}
	a.loop.sendNotices(v, st)
	a.loop.saveWork(v, st)
	a.loop.jobs(v, st)
	st = a.loop.settle(v, st)
	if a.in == nil || st.Mode != state.ModeRunning {
		return
	}
	// Nothing draws an agent's screen: it is quiet once its hooks have been, and no human key holds it.
	v.LastOutput = latest(st.Run.LastActivity, st.Run.LastStop)
	a.loop.clock(v)
	a.loop.why, a.loop.wished = a.loop.gate(v, st), false
	a.loop.resolve(v, st)
	a.loop.remind(v, st)
	a.loop.watch(v, st, a.in)
}

// Ended lets the agent go: its sub-run's uncommitted work is saved once more, after any snapshot still
// running, and what it has for the human is sent.
func (a *Agent) Ended() {
	a.loop.Ended()
	a.loop.Flush()
}
