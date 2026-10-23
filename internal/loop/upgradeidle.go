package loop

import (
	"slices"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A running plan's lead restarts on a newer baton at one of two points, and only with no phase agent live,
// since a restart ends every agent in its claude: a compaction baton is about to type (Tick), or, when the
// lead holds no phase and so has no compaction coming, as soon as it is idle (idleRestart). New agents are
// held for the restart only while the lead can reach a compaction by itself (upgradeHolds). In a real run
// (v0.8.14) the newer baton held every new agent, and the lead sat idle, waiting on a phase whose fresh
// agent was held: nothing moved until the human came back.

// idleWhy is why a lead idle with no phase in hand restarts, as the restarting event says.
const idleWhy = "idle, waiting on agents"

// idleRestart restarts the lead on a newer baton at once, while the plan runs, when it has no compaction
// of its own to restart at and nothing a restart would end is under way (idleForUpgrade), and nothing else
// holds a restart (restartable, with the roster the judge of the phase agents: rosterJudged). The
// restarted host takes up the workers whose agents ended (Coordinator.respawn). It reports whether claude
// was asked to end.
func (l *Loop) idleRestart(v host.View, st state.State, in host.Injector) bool {
	if !l.upgradeDue(v, st) || !idleForUpgrade(st) || !l.restartable(v, rosterJudged(st), l.why) {
		return false
	}
	l.restartOn(v, st, in, idleWhy)
	return true
}

// rosterJudged is st with baton's phase agents taken off the background work the lead's last Stop listed,
// for the roster to judge (phaseAgentsAtWork). An idle lead has no Stop to bring that list up to date, so
// it still names the agents baton has stopped since: an agent that checkpointed stays on it, and its
// worker, suspended for a fresh agent, still carries its id, so State.PhaseAgents counts it at work. The
// rest of the list (the model's own work, its monitors) still holds a restart.
func rosterJudged(st state.State) state.State {
	st.Run.Background = slices.DeleteFunc(slices.Clone(st.Run.Background), func(t state.Task) bool { return t.AgentType == state.PhaseAgent })
	return st
}

// idleForUpgrade reports a running plan whose lead has no point of its own to restart at: it holds no
// phase and its turn is over, no compaction is owed or queued (that one is the restart point, Tick's), and
// no phase agent is at work (phaseAgentsAtWork). A failed compaction nothing owes any more is never typed
// again, so it holds nothing.
func idleForUpgrade(st state.State) bool {
	switch {
	case st.Mode != state.ModeRunning || st.InHand() || st.Run.TurnOpen:
		return false
	case st.BoundaryOwed || st.CheckpointOwed || st.Run.Compaction.Status == state.CompactQueued:
		return false
	}
	return !phaseAgentsAtWork(st)
}

// phaseAgentsAtWork reports a phase agent a restart would end, as the roster has it: one starting, at work,
// or idle with its work in the merge queue, whose landing a restart would set back (Coordinator.Ended
// suspends it). The host keeps the roster as it starts and stops each agent; the lead's last Stop lists
// none started since, and keeps those stopped since (rosterJudged). A worker suspended or held has no
// agent at work: baton stops a suspended one's (Coordinator.afresh), and a held one's the human stopped.
func phaseAgentsAtWork(st state.State) bool {
	for _, w := range st.Workers {
		switch w.Status {
		case state.WorkerStarting, state.WorkerWorking, state.WorkerLanding:
			return true
		}
	}
	return false
}

// upgradeHolds reports that a newer baton holds new phase agents (Coordinator.pending): only while the
// lead can reach a compaction to restart at by itself, holding a phase, or with a compaction owed or
// queued (a boundary is owed only with a next phase taken up: state.advance). Otherwise agents start, and
// the lead restarts once none is at work (idleRestart): holding them then would hold the run for good. A
// run that is not running starts no agent anyway, and is held as before.
func (l *Loop) upgradeHolds(v host.View, st state.State) bool {
	if !l.upgradeDue(v, st) {
		return false
	}
	return st.Mode != state.ModeRunning || st.InHand() || st.BoundaryOwed || st.CheckpointOwed ||
		st.Run.Compaction.Status == state.CompactQueued
}
