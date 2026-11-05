package loop

import (
	"cmp"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A running plan's lead restarts on a newer baton at one of two points, and only with no phase agent live,
// since a restart ends every agent in its claude: a compaction baton is about to type (Tick), or, when the
// lead holds no phase and so has no compaction coming, as soon as it is idle (idleRestart). New agents are
// held for the restart only while the lead can reach a compaction by itself and phase agents are all that
// would keep it away, and for Timing.UpgradeWait at most (upgradeHolds). In a real run (v0.8.14) the newer
// baton held every new agent, and the lead sat idle, waiting on a phase whose fresh agent was held: nothing
// moved until the human came back. In a 74-phase campaign (v0.8.20) a background poll loop of the model's
// own, 22 hours old, kept every restart away, and the hold, with no deadline, kept new agents from starting
// for as long: four phases' agents waited, about 8 agent-hours. So the hold has a deadline, holding agents
// never waits on what it cannot clear, and what keeps a restart away is said (upgradeWaits).

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
	st.Run.Background = slices.DeleteFunc(slices.Clone(st.Run.Background), func(t state.Task) bool { return state.IsPhaseAgent(t.AgentType) })
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
// queued (a boundary is owed only with a next phase taken up: state.advance), and phase agents are all that
// would keep the restart away there. Holding agents cannot clear the model's own background work, its
// wakeups or its monitors, so with any of those under way no agent is held; a gate's reason (the lead's
// turn, a draft, a dialog) is the moment's and passes by itself, so it holds nothing back. The hold lasts
// Timing.UpgradeWait at most for a version (upgradeGivenUp): then agents start again, and the restart
// comes at the first point the run reaches with nothing in the way. Otherwise agents start, and the lead
// restarts once none is at work (idleRestart): holding them then would hold the run for good. A run that
// is not running starts no agent anyway, and is held as before.
func (l *Loop) upgradeHolds(v host.View, st state.State) bool {
	if !l.upgradeDue(v, st) {
		return false
	}
	if st.Mode != state.ModeRunning {
		return true
	}
	if !st.InHand() && !st.BoundaryOwed && !st.CheckpointOwed && st.Run.Compaction.Status != state.CompactQueued {
		return false
	}
	if blocking(st, "").ownWork() {
		return false
	}
	w := l.upgradeFor(l.Newer())
	switch {
	case w.gaveUp:
		return false
	case w.heldAt.IsZero():
		w.heldAt = v.Now
	}
	if v.Now.Sub(w.heldAt) < l.upgradeWait() {
		return true
	}
	l.upgradeGivenUp(v, st, w)
	return false
}

// upgradeWait is how long a newer baton may hold new agents, and keep a restart away unsaid
// (Timing.UpgradeWait, 10 minutes when unset).
func (l *Loop) upgradeWait() time.Duration {
	if l.Timing.UpgradeWait > 0 {
		return l.Timing.UpgradeWait
	}
	return 10 * time.Minute
}

// upgradeWait is the lead's wait for a restart on one newer baton: when new phase agents were first held
// for it (upgradeHolds), and whether that hold ran past Timing.UpgradeWait (upgrade_given_up); since when
// each kind of thing has kept the restart away without a break, and the kinds logged (upgrade_waits) and
// told to the human. A version installed after it starts the wait over.
type upgradeWait struct {
	version string
	heldAt  time.Time
	gaveUp  bool
	since   map[string]time.Time
	logged  map[string]bool
	told    map[string]bool
}

// upgradeFor is the wait for version, a fresh one for a version the loop did not wait for yet.
func (l *Loop) upgradeFor(version string) *upgradeWait {
	if l.up.version != version || l.up.since == nil {
		l.up = upgradeWait{version: version, since: map[string]time.Time{}, logged: map[string]bool{}, told: map[string]bool{}}
	}
	return &l.up
}

// upgradeGivenUp lets new agents start again: the hold for w's version ran its Timing.UpgradeWait, and
// holding them longer would hold the run (upgrade_given_up, told to the human once).
func (l *Loop) upgradeGivenUp(v host.View, st state.State, w *upgradeWait) {
	w.gaveUp = true
	waited := v.Now.Sub(w.heldAt).Round(time.Second)
	agents := atWork(st)
	l.Store.Event("upgrade_given_up", map[string]any{"version": w.version, "waited": waited.String(), "agents": agents})
	l.update(func(st *state.State) {
		st.Run.Announce = append(st.Run.Announce, decide.UpgradeGivenUp(w.version, l.upgradeWait(), agents))
	})
}

// The kinds of thing that keep a restart away, as upgrade_waits logs them.
const (
	upTasks    = "background"
	upAgents   = "agents"
	upWakeups  = "wakeups"
	upMonitors = "monitors"
	upGate     = "gate"
)

// upgradeBlock is what keeps a restart on a newer baton away, as restartable reads it: the model's own
// background work (shells, subagents, workflows), the phases whose agents are at work, the session's
// wakeups and monitors, and the gate's reason when it is one that may last (a dialog open, the usage
// limit, the mod's hello). The moment's own gates (the lead's turn, the human typing or their draft, the
// screen) pass by themselves and are left out.
type upgradeBlock struct {
	tasks             []state.Task
	agents            []string
	wakeups, monitors int
	gate              string
}

// blocking is what keeps a restart away in st, with why the gate's reason.
func blocking(st state.State, why string) upgradeBlock {
	b := upgradeBlock{tasks: st.Run.BusyBackground(), agents: atWork(st), wakeups: st.Run.Crons, monitors: len(st.Run.Watching())}
	switch why {
	case gateDialog, gateUsage, gateMod:
		b.gate = why
	}
	return b
}

// ownWork reports something in b that holding new agents cannot clear: the model's own background work,
// wakeups and monitors. (A gate's reason passes by itself, and phase agents end as their phases do.)
func (b upgradeBlock) ownWork() bool {
	return len(b.tasks) > 0 || b.wakeups > 0 || b.monitors > 0
}

// kinds lists what b holds, by kind.
func (b upgradeBlock) kinds() []string {
	var out []string
	if len(b.tasks) > 0 {
		out = append(out, upTasks)
	}
	if len(b.agents) > 0 {
		out = append(out, upAgents)
	}
	if b.wakeups > 0 {
		out = append(out, upWakeups)
	}
	if b.monitors > 0 {
		out = append(out, upMonitors)
	}
	if b.gate != "" {
		out = append(out, upGate)
	}
	return out
}

// atWork lists the phases whose agents a restart would end: the roster's at work (phaseAgentsAtWork),
// else the phase agents the lead's last Stop listed, by their description.
func atWork(st state.State) []string {
	var out []string
	for _, w := range st.Workers {
		switch w.Status {
		case state.WorkerStarting, state.WorkerWorking, state.WorkerLanding:
			out = append(out, w.Phase)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, t := range st.PhaseAgents() {
		out = append(out, cmp.Or(t.Description, t.ID))
	}
	return out
}

// upgradeWaits says what keeps a restart on a newer baton away, once a kind of it has done so without a
// break for Timing.UpgradeWait: upgrade_waits once per version and kind, and the human told once per
// version and kind, naming everything that holds the restart then. Phase agents alone are logged and not
// told: they end as their phases do, and a hold for them is told as it gives up (upgradeGivenUp). In a
// 74-phase campaign this would have named, ten minutes after the newer baton was installed, the poll loop
// that had kept every restart away for 22 hours.
func (l *Loop) upgradeWaits(v host.View, st state.State, why string) {
	if !l.upgradeDue(v, st) {
		return
	}
	w := l.upgradeFor(l.Newer())
	b := blocking(st, why)
	kinds := b.kinds()
	for k := range w.since {
		if !slices.Contains(kinds, k) {
			delete(w.since, k)
		}
	}
	var due []string
	for _, k := range kinds {
		since, ok := w.since[k]
		if !ok {
			w.since[k], since = v.Now, v.Now
		}
		if !w.logged[k] && v.Now.Sub(since) >= l.upgradeWait() {
			due = append(due, k)
		}
	}
	if len(due) == 0 {
		return
	}
	held := l.upgradeHeld(v, st, b)
	tell := false
	for _, k := range due {
		w.logged[k] = true
		fields := map[string]any{"version": w.version, "cause": k, "waited": v.Now.Sub(w.since[k]).Round(time.Second).String()}
		switch k {
		case upTasks:
			fields["tasks"] = upgradeTasks(held.Tasks)
		case upAgents:
			fields["agents"] = b.agents
		case upWakeups:
			fields["wakeups"] = b.wakeups
		case upMonitors:
			fields["monitors"] = b.monitors
		case upGate:
			fields["gate"] = b.gate
		}
		l.Store.Event("upgrade_waits", fields)
		if k != upAgents && !w.told[k] {
			w.told[k], tell = true, true
		}
	}
	if tell {
		l.update(func(st *state.State) {
			st.Run.Announce = append(st.Run.Announce, decide.UpgradeWaits(w.version, l.upgradeWait(), held))
		})
	}
}

// upgradeHeld is b as the human is told it: each background task with what names it, how long it has run
// (from when baton saw it start, was first told of it, or a Stop first listed it), and the phase it started
// in.
func (l *Loop) upgradeHeld(v host.View, st state.State, b upgradeBlock) decide.UpgradeHeld {
	h := decide.UpgradeHeld{Agents: b.agents, Wakeups: b.wakeups, Monitors: b.monitors, Gate: b.gate}
	for _, t := range b.tasks {
		from, phase := t.Since, ""
		if at := st.Run.BackgroundToldAt[t.ID]; !at.IsZero() && (from.IsZero() || at.Before(from)) {
			from = at
		}
		if b, ok := st.Run.BackgroundStarts[t.ID]; ok { // as baton saw it start: when, and in which phase
			phase = b.Phase
			if !b.At.IsZero() && (from.IsZero() || b.At.Before(from)) {
				from = b.At
			}
		}
		var age time.Duration
		if !from.IsZero() {
			age = v.Now.Sub(from)
		}
		h.Tasks = append(h.Tasks, decide.UpgradeTask{Type: t.Type, Description: t.Description, ID: t.ID, Age: age, Phase: phase})
	}
	return h
}

// upgradeTasks is the record of tasks for upgrade_waits.
func upgradeTasks(tasks []decide.UpgradeTask) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		f := map[string]any{"task": t.ID, "type": t.Type, "age_seconds": int(t.Age.Seconds())}
		if t.Description != "" {
			f["description"] = t.Description
		}
		if t.Phase != "" {
			f["phase"] = t.Phase
		}
		out = append(out, f)
	}
	return out
}
