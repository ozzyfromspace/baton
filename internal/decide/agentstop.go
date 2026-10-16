package decide

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent the human stopped is theirs to decide on, and only theirs: baton asks them whether to
// restart it, on the lead's screen, and if nobody answers it decides from the record (AgentStopAction).
// A first stop's agent is restarted unless its work is done; after a second, baton takes the agent down,
// and a phase whose work is not done waits for the human's answer while the rest of the run can go on.
// Once nothing else can move, baton takes the phase back from its agent, escalation_timeout later, and
// the lead does it in its session (Taken). These are the words of all that, for the human, the board,
// the fresh agent and the lead.

// The answers to a question about a stopped agent (state.RelayStopped), and about one baton restarted
// (state.RelayRestarted), as the human picks them.
const (
	RestartIt    = "Restart it"
	LeaveStopped = "Leave it stopped"
	StopIt       = "Stop it"
	KeepItGoing  = "Keep it going"
)

// What the mod answers a question about a stopped agent with at its deadline: baton then decides from the
// record (stoppedDeadline), or the question with no deadline steps aside, to come back (stepAside). The
// human never picks either.
const (
	stoppedDeadline = "Let baton decide"
	stepAside       = "Ask me later"
)

// What baton does about a phase agent the human stopped, given the answer to its question
// (AgentStopAction).
const (
	StopRestart  = "restart"   // a fresh agent takes the phase up, in its worktree
	StopLeave    = "leave"     // the phase waits for the human, its work kept: /baton run starts a fresh agent
	StopPause    = "pause"     // the run pauses, and the phase waits for the human
	StopRelease  = "release"   // its work is done: the agent is let go, and the work lands without one
	StopTakeDown = "take_down" // nobody answered a second stop: the agent is taken down, and the human asked with no deadline
	StopLater    = "later"     // the question with no deadline steps aside, and comes back once the human is
	StopKeep     = "keep"      // the agent baton restarted carries on
)

// agentOptions are the answers to question r about the agent on its phase (state.Relay.AboutAgent).
func agentOptions(r state.Relay) []Option {
	pause := Option{"Pause baton", "I'm taking over; baton stops driving the plan"}
	if r.Kind == state.RelayRestarted {
		return []Option{
			{StopIt, "End the fresh agent; " + r.Phase + " waits for me, and /baton run starts one again"},
			pause,
			{KeepItGoing, "Let it carry on with " + r.Phase},
		}
	}
	return []Option{
		{RestartIt, "A fresh agent takes " + r.Phase + " up in its worktree, from its notes"},
		{LeaveStopped, r.Phase + " waits for me, its work kept; /baton run starts a fresh agent on it"},
		pause,
	}
}

// agentDefault is what the mod answers question r about the agent on its phase with at its deadline.
func agentDefault(r state.Relay) string {
	switch {
	case r.Kind == state.RelayRestarted:
		return KeepItGoing
	case r.Persistent():
		return stepAside
	}
	return stoppedDeadline
}

// AgentStopAction is what baton does about the agent on the phase of question r, answered a (by the human,
// or by nobody: at its deadline, or its dialog gone). The human's answer is done as they chose; their own
// words, which no stopped agent can take, leave it stopped for them, and a restarted one carries on with
// them. Nobody's answer is decided from the record: an agent whose work is done (satisfied) is let go; a
// first stop's is restarted, and a second's taken down; the question with no deadline steps aside.
func AgentStopAction(r state.Relay, a state.Resolution, satisfied bool) string {
	human := a.By == state.ByHuman
	if r.Kind == state.RelayRestarted {
		switch {
		case human && a.Answer == StopIt:
			return StopLeave
		case human && a.Answer == "Pause baton":
			return StopPause
		}
		return StopKeep
	}
	switch {
	case human && a.Answer == RestartIt:
		return StopRestart
	case human && a.Answer == "Pause baton":
		return StopPause
	case human:
		return StopLeave
	case r.Persistent():
		return StopLater
	case satisfied:
		return StopRelease
	case r.Count >= 2:
		return StopTakeDown
	}
	return StopRestart
}

// agentQuestion is the text of question r about the agent on its phase, whose answer is due at deadline.
func agentQuestion(r state.Relay, deadline time.Time) string {
	name := r.Phase
	if r.What != "" {
		name += " (" + clause(r.What) + ")"
	}
	by := deadline.Local().Format("15:04")
	switch {
	case r.Kind == state.RelayRestarted:
		return fmt.Sprintf("baton: nobody answered about the agent you stopped on %s, and its work was not done, so baton restarted it: a fresh agent takes %s up "+
			"in its worktree. Keep it going? Unless you answer by %s, it carries on.", name, r.Phase, by)
	case r.Persistent():
		return fmt.Sprintf("baton: you stopped the agent on %s twice, and nobody answered, so baton took it down with %s not done. What should become of %s? "+
			"While the rest of the run can go on, baton will not choose for you: %s and the phases that need it wait for your answer. "+
			"Once nothing else can move, baton takes %s back from its agent, not before %s, and Claude does it in this session. "+
			"If you are away, this question steps aside and comes back when you next write here.", name, r.Phase, r.Phase, r.Phase, r.Phase, by)
	case r.Count >= 2:
		return fmt.Sprintf("baton: you stopped the agent on %s again. Restart it? Unless you answer by %s, baton takes it down, and if %s is not done, "+
			"asks you what to do with it while the rest of the run goes on.", name, by, r.Phase)
	}
	return fmt.Sprintf("baton: you stopped the agent on %s. Restart it? Unless you answer by %s, baton restarts it, or lets it go if its work is done.", name, by)
}

// AgentStopped tells the human that the agent on phase was stopped (stops: how many times now), and that
// baton asks them what they want of it.
func AgentStopped(phase string, stops int) string {
	if stops >= 2 {
		return fmt.Sprintf("baton: the phase agent on %s was stopped again (%s times), its work kept in its worktree: baton asks you once more whether to restart it",
			phase, strconv.Itoa(stops))
	}
	return fmt.Sprintf("baton: the phase agent on %s was stopped, its work kept in its worktree: baton asks you whether to restart it", phase)
}

// AgentRestarted tells the human that a fresh agent takes phase up in place of the one they stopped: as
// they chose (human), or as nobody answered and its work was not done.
func AgentRestarted(phase string, human bool) string {
	if human {
		return fmt.Sprintf("baton: a fresh phase agent takes %s up in its worktree, from its notes, as you chose", phase)
	}
	return fmt.Sprintf("baton: nobody answered about the agent you stopped on %s, and its work was not done, so baton restarted it: "+
		"a fresh agent takes %s up in its worktree, from its notes", phase, phase)
}

// AgentRestartedNotice is what the human is pushed when baton restarted the agent on phase for them.
func AgentRestartedNotice(phase string) string {
	return "nobody answered about the agent you stopped on " + phase + ", so baton restarted it"
}

// AgentRestartedWhy is why a fresh agent takes phase up, on the roster (Worker.Why).
func AgentRestartedWhy(human bool) string {
	if human {
		return "the human stopped the agent before it, and chose Restart it"
	}
	return "the human stopped the agent before it, and nobody answered whether to restart it"
}

// AgentRestartedTell tells the fresh agent on phase why it was started: the human stopped the one before
// it, and chose a fresh one (human) or did not answer.
func AgentRestartedTell(phase string, human bool) string {
	why := "nobody answered baton's question about it, and " + phase + " was not done, so baton started you in its place"
	if human {
		why = "then asked baton for a fresh agent: you"
	}
	return fmt.Sprintf("[baton] The human stopped the agent that worked on %s before you, and %s. Carry on with %s from its notes and its worktree, as your brief says.",
		phase, why, phase)
}

// AgentReleased tells the human that nobody answered about the agent they stopped on phase, and that its
// work is done, so baton let it go: its work lands without it, and its worker is retired once it has.
func AgentReleased(phase string) string {
	return fmt.Sprintf("baton: nobody answered about the agent you stopped on %s; its work is done, so baton let it go: %s's work lands without it, "+
		"and its worker is retired once it has", phase, phase)
}

// AgentReleasedNotice is what the human is pushed when baton let the agent they stopped on phase go.
func AgentReleasedNotice(phase string) string {
	return "nobody answered about the agent you stopped on " + phase + "; its work is done, so baton let it go"
}

// AgentNeededAgain is why a fresh agent takes up a phase whose stopped agent baton let go, its work done
// (AgentReleased): its work needs an agent again.
const AgentNeededAgain = "the agent the human stopped was let go with its work done, and its work needs an agent again"

// AgentWorkReopened tells the human that a fresh agent takes phase up, whose stopped agent baton let go
// with its work done: that work needs an agent again.
func AgentWorkReopened(phase string) string {
	return "baton: a fresh phase agent takes " + phase + " up: " + AgentNeededAgain
}

// AgentTakenDown tells the human that nobody answered about the agent they stopped twice on phase, whose
// work is not done: baton took it down, and phase waits for their answer while the rest of the run can go
// on; once nothing else can move, baton takes it back wait (escalation_timeout) later, for the lead.
func AgentTakenDown(phase string, wait time.Duration) string {
	return fmt.Sprintf("baton: nobody answered about the agent you stopped twice on %s, and %s is not done: baton took the agent down, its work kept in its worktree. "+
		"%s and the phases that need it wait for your answer while the rest of the run goes on; once nothing else can move, baton waits %s more, "+
		"then takes %s back, and Claude does it in this session", phase, phase, phase, Spell(wait), phase)
}

// AgentTakenDownNotice is pushed to the human as phase waits for their answer (AgentTakenDown).
func AgentTakenDownNotice(phase string) string { return phase + ": " + NeedsAgentStoppedTwice }

// AgentTakenDownWhy is why a worker whose agent baton took down is held (Worker.Why).
const AgentTakenDownWhy = "the human stopped its agent twice, and nobody answered whether to restart it"

// AgentPersistent is why the question about a phase agent baton took down has no deadline
// (state.Relay.Persist).
const AgentPersistent = "the human stopped its agent twice, and only they can say what they want of its phase"

// AgentLeft tells the human that the agent on phase stays stopped, as they chose: phase waits for them.
func AgentLeft(phase string) string {
	return fmt.Sprintf("baton: the agent on %s stays stopped, as you chose: %s waits for you, its work kept in its worktree; /baton run starts a fresh agent on it",
		phase, phase)
}

// AgentLeftWhy is why a worker stays held, its agent stopped, as the human chose (Worker.Why).
const AgentLeftWhy = "the human stopped its agent, and chose to leave it stopped"

// AgentStoppedAgain tells the human that baton ended the agent it restarted on phase, as they chose: phase
// waits for them.
func AgentStoppedAgain(phase string) string {
	return fmt.Sprintf("baton: the agent baton restarted on %s is stopped, as you chose: %s waits for you, its work kept in its worktree; "+
		"/baton run starts a fresh agent on it", phase, phase)
}

// AgentWordsTell gives the agent baton restarted on phase the human's own answer to the question about it.
func AgentWordsTell(phase, answer string) string {
	return fmt.Sprintf("[baton] The human answered baton's question about you in their own words: %s. That is their instruction: follow it, then carry on with %s.",
		strconv.Quote(answer), phase)
}

// What a phase whose agent the human stopped needs of them (Stand.Why): baton asks whether to restart it,
// it waits for them after they left it stopped, or after baton took it down twice-stopped.
const (
	NeedsAgentAsked        = "its agent was stopped; baton asks you whether to restart it"
	NeedsAgentStoppedTwice = "its agent was stopped twice; waiting for your answer"
)

// AgentStoppedStand is what the phase of worker w, held as the human stopped its agent, needs of them:
// asking says a question about it waits to be answered.
func AgentStoppedStand(w state.Worker, asking bool) string {
	switch {
	case !w.TakenDown.IsZero():
		return NeedsAgentStoppedTwice
	case asking:
		return NeedsAgentAsked
	}
	return NeedsAgentStopped
}

// A twice-stopped phase the run cannot go on without: with nothing else able to move, baton takes it back
// from its agent escalation_timeout later (loop/stopped.go), and the lead takes it up at a boundary.

// StoppedTakenAnswer closes the question about a twice-stopped phase that baton took back for the lead
// (state.Relay.Answered, by timeout). Nobody picks it.
const StoppedTakenAnswer = "Taken back for the lead"

// StoppedTakenWhy is why the worker of a twice-stopped phase that baton took back for the lead ended
// (Worker.Why).
const StoppedTakenWhy = "the human stopped its agent twice and did not answer, and nothing else in the run could move: the lead takes its phase up"

// Taken is a twice-stopped phase baton took back for the lead: the decision that records it (ID), the
// branch its agent's Unlanded commits stay on ("" for none), the snapshot of what it had not committed
// ("" for none), and its worktree, left in place for the reason Kept gives ("" when it was removed).
type Taken struct {
	ID, Phase      string
	Branch         string
	Unlanded       int
	Snapshot       string
	Worktree, Kept string
}

// kept says where t's agent's work stays, as clauses that follow a sentence ("; its agent's 3 commits stay
// on branch b"), or "" when nothing of it stays.
func (t Taken) kept() string {
	s := ""
	if t.Branch != "" && t.Unlanded > 0 {
		s += fmt.Sprintf("; its agent's %d %s %s on branch %s", t.Unlanded, plural(t.Unlanded, "commit", "commits"), plural(t.Unlanded, "stays", "stay"), t.Branch)
	}
	if t.Snapshot != "" {
		s += "; what it had not committed is saved as " + t.Snapshot
	}
	if t.Kept != "" {
		s += fmt.Sprintf("; its worktree stays at %s, as %s", t.Worktree, t.Kept)
	}
	return s
}

// StoppedTaken tells the human, in the lead's session, that baton took t.Phase back from the agent they
// stopped twice, as decision t.ID ("" when none could be recorded): nobody answered, and nothing else in
// the run could move.
func StoppedTaken(t Taken) string {
	noted := "baton: "
	if t.ID != "" {
		noted = "baton: noted (" + t.ID + ") — "
	}
	return fmt.Sprintf("%snobody answered about the agent you stopped twice on %s, and nothing else in the run could move, "+
		"so baton took %s back from it: Claude takes %s up in this session after a compaction%s. /baton pause stops it",
		noted, t.Phase, t.Phase, t.Phase, t.kept())
}

// StoppedTakenNotice is what the human is pushed when baton took phase back from the agent they stopped
// twice.
func StoppedTakenNotice(phase string) string {
	return "nobody answered about the agent you stopped twice on " + phase + ", and the run could go on no other way, so Claude takes " +
		phase + " up in the lead's session"
}

// StoppedTakenDecision is the decision baton records as it takes t.Phase back from the agent the human
// stopped twice: what it did, and how to undo it.
func StoppedTakenDecision(t Taken) (what, undo string) {
	what = fmt.Sprintf("took %s back from its agent for Claude to do in the lead's session: the human stopped the agent twice and did not answer "+
		"what should become of %s, and nothing else in the run could move%s", t.Phase, t.Phase, t.kept())
	undo = fmt.Sprintf("/baton pause stops the run before Claude goes further with %s; then say what should become of it", t.Phase)
	return what, undo
}

// StoppedTakenBrief is the note on t.Phase that the lead's brief quotes as it takes the phase up: why it
// is the lead's now, and where its agent's work is.
func StoppedTakenBrief(t Taken) string {
	s := fmt.Sprintf("[baton] The human stopped the phase agent on %s twice and did not answer baton's question about it, and nothing else in the run could move, "+
		"so baton took %s back from its agent: you do %s here, in the lead's session. The human gave no reason, so look at what the agent did before you build on it.",
		t.Phase, t.Phase, t.Phase)
	if t.Branch != "" && t.Unlanded > 0 {
		s += fmt.Sprintf(" Its %d %s %s on branch %s, not on the run's branch: to carry on from %s, merge that branch into the run's branch (git merge, never a rebase).",
			t.Unlanded, plural(t.Unlanded, "commit", "commits"), plural(t.Unlanded, "is", "are"), t.Branch, plural(t.Unlanded, "it", "them"))
	} else {
		s += " It left no commits that the run's branch lacks."
	}
	if t.Snapshot != "" {
		s += fmt.Sprintf(" What it had not committed is saved as %s: git show %s shows it.", t.Snapshot, t.Snapshot)
	}
	if t.Kept != "" {
		s += fmt.Sprintf(" Its worktree stays at %s, as %s.", t.Worktree, t.Kept)
	}
	return s
}

// StoppedRunWaits is the lead's stop line with no phase in hand when the run waits on nothing but the
// human's answer about the agent they stopped twice on phase: unless they answer, baton takes phase back
// at at, for the lead.
func StoppedRunWaits(phase string, at time.Time) string {
	return fmt.Sprintf("baton: no phase can start yet: the run waits on your answer about the agent you stopped twice on %s — unless you answer, "+
		"baton takes %s back from it at %s, and Claude does it here", phase, phase, at.Local().Format("15:04"))
}

// StoppedPhaseWaits is added to the lead's stop line with no phase in hand when phase waits on the human's
// answer about the agent they stopped twice on it, and something else in the run can still move: once
// nothing can, baton takes phase back wait (escalation_timeout) later, for the lead.
func StoppedPhaseWaits(phase string, wait time.Duration) string {
	return fmt.Sprintf("baton: %s waits on your answer about the agent you stopped twice on it; once nothing else in the run can move, baton waits %s more, "+
		"then takes %s back, and Claude does it here", phase, Spell(wait), phase)
}
