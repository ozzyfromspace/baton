package decide

import (
	"fmt"
	"time"
)

// baton's mod is JavaScript that Claude Code runs in the lead's own process (plugin/hooks/baton.js): it
// starts and steers the phase agents that work beside the lead, on the host's say-so (internal/bridge).
// These are the words it and the host use about that. The mod words nothing itself.

// PhaseAgentDescription is the baton:phase agent type's line in Claude Code's list of agent types, which
// the models of the session see (baton refuses their calls to it: decide.PhaseTypeIsBatons).
const PhaseAgentDescription = "A phase of a plan baton runs, in a git worktree of its own. Only baton starts these: never use this type yourself."

// PhaseAgentPrompt is the baton:phase agent type's system prompt. Its brief, which says what to do, comes
// from baton when it starts.
const PhaseAgentPrompt = "You work on one phase of a plan that baton runs, in a git worktree of your own, beside the lead session and any other phase agents. " +
	"baton gives you your brief when you start, and its messages as you go: follow them, work only in your worktree, and report through the baton command as the brief says."

// BoardCommand is the line of baton's board command (/baton-board) in Claude Code's list of commands. It
// opens the board while Claude works; /baton board, the skill's, waits for the turn to end.
const BoardCommand = "baton's board: the plan step by step, each phase and the lead (opens while Claude works)"

// BoardTitle is the title of the board's pane.
const BoardTitle = "baton"

// ReportKept is the line the human reads in the lead's transcript when baton's mod keeps a report of the
// agent on phase from the lead's model: baton takes the agent's reports, and tells the lead what it needs.
func ReportKept(phase string) string {
	return fmt.Sprintf("%s's report went to baton, not to Claude", phase)
}

// Serial says, once, that a run's phases go one at a time in the lead session, and why (ModMissing or
// ModMismatch).
func Serial(why string) string {
	return "baton: phases run one at a time here: " + why
}

// ModMissing is why phases run one at a time when baton's mod never said hello: Claude Code did not load
// it, which an organization's settings can forbid, or an older Claude Code cannot.
const ModMissing = "Claude Code did not load baton's mod, which starts phase agents beside this session"

// ModMismatch is why phases run one at a time when the mod Claude Code loaded is of another baton:
// version is its plugin's, and running the baton this session runs.
func ModMismatch(version, running string) string {
	if version == "" {
		version = "unknown"
	}
	return fmt.Sprintf("Claude Code loaded baton's mod from plugin %s, which does not work with this session's baton (%s)", version, running)
}

// AgentDescription is a phase agent's line in Claude Code's agent panel: the phase and its title.
func AgentDescription(phase, title string) string {
	if title == "" {
		return phase
	}
	return phase + " — " + title
}

// AgentBegin is the task a phase agent is started with. Its brief reaches it as it starts (SubagentStart).
func AgentBegin(phase string) string {
	return fmt.Sprintf("[baton] Begin %s. baton's brief, given to you as you start, has your instructions.", phase)
}

// CompactVetoed is why a compaction of the lead did not happen when a hook of another plugin vetoed it.
const CompactVetoed = "a hook vetoed the compaction"

// ModSilent is why baton asks again for a compaction of the lead when its mod did not take the command up
// within wait.
func ModSilent(wait time.Duration) string {
	return fmt.Sprintf("baton's mod did not take the compaction up within %s", Spell(wait))
}

// ModNoWord is why a compaction of the lead failed when nothing said within wait that it began or ended.
func ModNoWord(wait time.Duration) string {
	return fmt.Sprintf("nothing said within %s that the compaction baton's mod was asked for began", Spell(wait))
}

// ModCompactFailed is why a round of compactions of the lead failed: baton's mod tried tries times, and the
// last try did not compact it, for why.
func ModCompactFailed(tries int, why string) string {
	return fmt.Sprintf("baton's mod could not compact the context (%d tries): %s", tries, why)
}

// DraftHolds is the notice that the human's draft in the input box holds baton, which needs to do task,
// while an agent may be at work: clearing the box (Ctrl-C) would stop every agent of the session.
func DraftHolds(task string) string {
	return fmt.Sprintf("baton needs to %s, and your draft in the input box holds it: baton does not clear it while an agent is at work, since that would stop the agent. Send or clear it yourself", task)
}
