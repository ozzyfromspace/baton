package loop

import (
	"os"
	"time"

	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// Agent is the loop of one phase agent this host started, which the coordinator ticks at each step of its
// work on the workers. A phase agent has no session of its own to tick a Loop: its hooks, routed to its
// sub-run, keep what its conversation does (its turns, its activity, its stops), and Agent does the rest,
// with the lead's own code: it keeps the sub-run's claim fresh (the owner's heartbeat, which tells every
// other baton process that the worker is at work), runs the sub-run's jobs and settles them, takes its
// phase up again when its verification reopened it (Loop.retake: nothing compacts an agent, so it owes no
// boundary), saves the worktree's uncommitted work when the sub-run halts, and sends the human the
// sub-run's notices. Once the
// agent is at work, its watchdog is the lead's (Loop.watch), reaching it through its line (line.go) as the
// lead's reaches the lead through the keyboard: an idle agent is woken for its results and baton's
// messages, reminded when it stops with its phase unreported, and its turn retried after an API error; a
// refused turn is escalated, never retried. What its hooks cannot say, its own transcript does (read): the
// size of its context, for its context valve, and its compactions, each proven there, after which it is
// given its brief again.
type Agent struct {
	Phase string
	Store *state.Store
	lead  *state.Store // the lead's run, whose context window is the agent's too
	loop  *Loop        // the sub-run's jobs, their settling, the snapshot of a halt and the watchdog, as a Loop does them
	beat  time.Time
	panic time.Time     // when a panic in its tick was last logged
	in    host.Injector // its agent's line, once the agent is at work, until baton ends it; nil otherwise
	// from is where the last look for a compaction in its transcript stopped; contextAt is when its
	// context was last read; rebriefed is the brief it was last woken for, at rebriefAt.
	from                 int64
	contextAt            time.Time
	rebriefed, rebriefAt time.Time
	// advise is the advisor's part of its tick, the coordinator's (advisor.go; nil: none).
	advise func(now time.Time, s *state.Store, st state.State) bool
}

// newAgent is the loop of the agent on phase, whose sub-run is sub, with the lead's settings.
func (c *Coordinator) newAgent(phase string, sub *state.Store) *Agent {
	lead := c.Lead
	return &Agent{Phase: phase, Store: sub, lead: lead.Store, advise: c.advise, loop: &Loop{Store: sub, Notify: lead.Notify, Project: lead.Project + " · " + phase,
		Timing: lead.Timing, Snapshot: lead.Snapshot, Version: lead.Version, Runners: lead.Runners, RunUsage: c.runUsage, RunPaused: c.paused.Load,
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
	st = a.loop.retake(v, st)
	st = a.read(now, st)
	if a.advise != nil && a.advise(now, a.Store, st) {
		if fresh, err := a.Store.Load(); err == nil {
			st = fresh // a look-in held for the advisor is not typed for (watch, deliver)
		}
	}
	if a.in == nil || st.Mode != state.ModeRunning {
		return
	}
	// Nothing draws an agent's screen, and no human key holds it: its gate skips the screen (Loop.gate).
	a.loop.clock(v)
	a.loop.why, a.loop.wished = a.loop.gate(v, st), false
	a.loop.resolve(v, st)
	a.loop.remind(v, st)
	if a.rebrief(v, st) {
		return
	}
	a.loop.watch(v, st, a.in)
}

// contextEvery is how often an agent's context is read from its transcript: often enough for its
// checkpoint to come well before Claude Code compacts it, seldom enough for a transcript of megabytes.
const contextEvery = 10 * time.Second

// read reads the agent's own transcript (Runtime.Transcript) for what its hooks cannot say, and returns
// the sub-run's state as it then stands. A compaction of its conversation is proven there and nowhere else
// (state.AgentBoundary: the compaction hooks it fires look like the lead's), and owes it its brief again,
// given as it goes on (its next tool call, or rebrief). Every contextEvery, the size of its context is
// recorded against the lead's window (an agent compacts where the lead does, spike 32), for its context
// valve, which its hooks run at its tool calls.
func (a *Agent) read(now time.Time, st state.State) state.State {
	if st.Mode != state.ModeRunning || st.Run.Transcript == "" {
		return st
	}
	p, next, proven := state.AgentBoundary(st, a.from)
	a.from = next
	if proven {
		var checkpoint, counted bool
		up, err := a.Store.Update(func(st *state.State) error {
			if checkpoint, counted = state.AgentCompacted(st, p, now); counted && st.Mode == state.ModeRunning {
				kind := brief.Auto
				if checkpoint {
					kind = brief.Checkpoint
				}
				st.Run.Briefing = &state.Briefing{Kind: kind, Phase: st.Current, At: now}
			}
			return nil
		})
		if err == nil && counted {
			st = up
			a.Store.Event("agent_compacted", map[string]any{"agent": st.Run.Briefed, "checkpoint": checkpoint, "proof": "transcript"})
		}
	}
	if now.Sub(a.contextAt) < contextEvery {
		return st
	}
	a.contextAt = now
	lead, err := a.lead.Load()
	window := lead.Run.Context
	if err != nil || window == nil || window.WindowSize <= 0 {
		return st // the lead's status line has not said yet
	}
	path := st.Run.Transcript
	tokens, at, err := transcript.Context(path, true)
	if err != nil || tokens <= 0 {
		return st
	}
	if c := st.Run.Context; c != nil && c.Tokens == tokens && c.WindowSize == window.WindowSize && c.Limit == window.Limit {
		return st
	}
	if at.IsZero() {
		at = now
	}
	use := &state.ContextUse{UsedPct: 100 * float64(tokens) / float64(window.WindowSize), Tokens: tokens, WindowSize: window.WindowSize, Limit: window.Limit, At: at}
	if up, err := a.Store.Update(func(st *state.State) error {
		if st.Run.Transcript == path {
			st.Run.Context = use
		}
		return nil
	}); err == nil {
		st = up
	}
	return st
}

// rebrief wakes the agent for the brief a compaction of its own owes it (Runtime.Briefing), once it is
// idle: an agent compacts inside a turn, and one that ends the turn before its next tool call would go on
// without it. Its start then gives it (hooks), once. The line is sent again only if the brief is still
// owed a while later (Timing.ResumeNudge), as a delivery's is. It reports whether it sent it.
func (a *Agent) rebrief(v host.View, st state.State) bool {
	b := st.Run.Briefing
	if b == nil || st.Run.TurnOpen || a.loop.why != "" || b.At.Equal(a.rebriefed) && v.Now.Sub(a.rebriefAt) < a.loop.Timing.ResumeNudge {
		return false
	}
	if err := a.in.Type(decide.AgentBriefAgain(st.Current), true); err != nil {
		return false // the message before it is not acked yet: sent at a later tick
	}
	a.rebriefed, a.rebriefAt = b.At, v.Now
	a.Store.Event("rebrief_sent", map[string]any{"agent": st.Run.Briefed, "type": b.Kind})
	return true
}

// Ended lets the agent go: its sub-run's uncommitted work is saved once more, after any snapshot still
// running, and what it has for the human is sent.
func (a *Agent) Ended() {
	a.loop.Ended()
	a.loop.Flush()
}
