package loop

import (
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Agent is the loop of one phase agent this host started, which the coordinator ticks at each step of its
// work on the workers. A phase agent has no session of its own to tick a Loop: its hooks, routed to its
// sub-run, keep what its conversation does (its turns, its activity, its stops), and Agent does the rest of
// what a worker session's host did for it: it keeps the sub-run's claim fresh (the owner's heartbeat, which
// tells every other baton process that the worker is at work), runs the sub-run's jobs and settles them,
// saves the worktree's uncommitted work when the sub-run halts, and sends the human the sub-run's notices.
type Agent struct {
	Phase string
	Store *state.Store
	loop  *Loop // the sub-run's jobs, their settling and the snapshot of a halt, as a Loop does them
	beat  time.Time
	panic time.Time // when a panic in its tick was last logged
}

// newAgent is the loop of the agent on phase, whose sub-run is sub, with the lead's settings.
func (c *Coordinator) newAgent(phase string, sub *state.Store) *Agent {
	lead := c.Lead
	return &Agent{Phase: phase, Store: sub, loop: &Loop{Role: RoleWorker, Store: sub, Notify: lead.Notify, Project: lead.Project + " · " + phase,
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
	a.loop.settle(v, st)
}

// Ended lets the agent go: its sub-run's uncommitted work is saved once more, after any snapshot still
// running, and what it has for the human is sent.
func (a *Agent) Ended() {
	a.loop.Ended()
	a.loop.Flush()
}
