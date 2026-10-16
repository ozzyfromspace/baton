package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase's id is the name of baton's agent on it, the human's handle on that agent, so no agent of a
// model's own takes one: Claude Code matches an agent's name ignoring case, and gives a name to the last
// agent that took it (plan's names.go).

// AgentNameIsPhase is why a model's own Agent call named name, which addresses phase's agent (the same id,
// ignoring case), is not run.
func AgentNameIsPhase(name, phase string) string {
	how := ""
	if name != phase {
		how = " (Claude Code matches an agent's name ignoring case)"
	}
	return fmt.Sprintf("[baton] Not run: %q names baton's agent on phase %s of the plan%s. That name is the human's handle on baton's agent, "+
		"and an agent of yours by it would take it over. Give your agent a name that is none of the plan's phase ids, or leave the name out, and run it again.", name, phase, how)
}

// The human writes to a phase's agent from the lead's prompt: `@P23 <words>` (state.HumanWords). Words
// for an agent at work are kept from the lead's model and sent to the agent; words for a phase no agent
// works on go to the lead's model, with the prompt, saying where the phase stands (state.Addressee).

// Handle is how the human writes to the agent on phase: at the lead's prompt.
func Handle(phase string) string { return "@" + phase + " <your words>" }

// SentToAgent is what the human reads once their prompt for the agent on phase was queued for it and kept
// from the lead's model: Claude Code shows it as why it blocked the prompt, under its own frame
// ("UserPromptSubmit operation blocked by hook:", then this, then "Original prompt: …"), and shows nothing
// else (spike 40), so it says what baton did.
func SentToAgent(phase string) string {
	return fmt.Sprintf("baton: sent to %s — its agent gets your words at its next tool call (an idle one wakes for them and goes on with %s); "+
		"Claude here does not see them, and baton empties the input box in a moment", phase, phase)
}

// HumanToAgent is the message that gives the agent on phase the human's words, as they wrote them at the
// lead's prompt.
func HumanToAgent(phase, words string) string {
	return fmt.Sprintf("[baton] The human wrote to you from the lead's terminal (@%s at its prompt). Their words, as they wrote them:\n\n%s\n\n"+
		"They are the human's, not baton's: do what they ask, within the plan's standing rules, then carry on with %s. What you write in reply "+
		"shows in your own view in Claude Code's agent panel, where they can read it.", phase, words, phase)
}

// NoAgent is why no agent of baton's takes the human's words for phase, which stands as to says
// (state.Addressee), in a few words.
func NoAgent(phase, to string) string {
	switch to {
	case state.ToPending:
		return phase + " has not started yet"
	case state.ToDone:
		return phase + " is done"
	case state.ToLanded:
		return phase + " is done, and its agent's work landed on the run's branch"
	case state.ToLead:
		return "the lead works on " + phase + " itself"
	case state.ToStopped:
		return phase + "'s agent was stopped from Claude Code's agent panel, and " + phase + " waits for the human (/baton run starts a fresh agent)"
	case state.ToStarting:
		return phase + "'s agent is still starting"
	case state.ToReplaced:
		return phase + "'s agent ended, and a fresh one is about to take it up"
	case state.ToLanding:
		return phase + "'s agent finished it, and its work waits to land"
	case state.ToRetiring:
		return phase + "'s agent is being let go"
	case state.ToUnknown:
		return "baton's mod does not have " + phase + "'s agent"
	}
	return "no agent of baton's is at work on " + phase
}

// WordsToLead tells the human that their prompt for the agent on phase went to the lead's model with the
// rest of it, as no agent takes it (why: NoAgent).
func WordsToLead(phase, why string) string {
	return fmt.Sprintf("baton: no phase agent takes your words for %s (%s), so they went to Claude here", phase, why)
}

// WordsForLead tells the lead's model that the prompt it has, which starts with phase's handle, is its
// own: no agent of baton's takes it (why: NoAgent).
func WordsForLead(phase, why string) string {
	return fmt.Sprintf("[baton] The human's prompt starts with @%s, which writes to baton's phase agent on %s, but no agent takes it: %s. "+
		"So the prompt is yours, as the human's words about %s: act on it as you would on any prompt of theirs.", phase, phase, why, phase)
}

// WordsSummary labels the message that gives an agent the human's words, in the lead's transcript.
const WordsSummary = "the human's words"

// WordsGone is why the human's words did not reach the agent they were for: it ended, or was stopped,
// before baton could send them.
const WordsGone = "its agent ended or was stopped before baton could send them"

// WordsRefused is why the human's words did not reach the agent they were for: Claude Code refused the
// message (said).
func WordsRefused(said string) string {
	if said == "" {
		return "Claude Code refused the message"
	}
	return "Claude Code refused the message: " + said
}

// WordsUnsent is what the human reads (say) and the lead's model is given (tell) when the human's words
// for the agent on phase did not reach it after all (why), and go to the lead instead.
func WordsUnsent(phase, words, why string) (say, tell string) {
	say = fmt.Sprintf("baton: your words for %s did not reach its agent (%s), so they went to Claude here", phase, why)
	tell = fmt.Sprintf("[baton] The human wrote this for baton's phase agent on %s (@%s at your prompt), and it did not reach the agent (%s). Their words:\n\n%s\n\n"+
		"They are yours now, as the human's words about %s: act on them as you would on any prompt of theirs.", phase, phase, why, words, phase)
	return say, tell
}

// LetGoWhy is why baton let go of an agent on phase, which now stands as to says (state.Addressee).
func LetGoWhy(phase, to string) string {
	switch to {
	case state.ToLanded:
		return phase + " is done, and its work landed on the run's branch"
	case state.ToDone:
		return phase + " is done"
	case state.ToLead:
		return "the lead took " + phase + " up"
	case state.ToAgent, state.ToStarting, state.ToReplaced, state.ToStopped, state.ToLanding, state.ToUnknown:
		return "another agent of baton's took " + phase + " up in your place"
	}
	return "baton took " + phase + " back"
}

// LetGoRefused is why a tool call of an agent baton let go of from phase (why: LetGoWhy) is not run: the
// human woke it from its own view in Claude Code's agent panel, which baton does not see.
func LetGoRefused(phase, why string) string {
	return fmt.Sprintf("[baton] Not run: baton let you go from %s (%s), so nothing of it is yours to do any more. Change nothing: end your turn now, "+
		"with no further tool call. If the human wrote to you, say in your reply that baton let you go, and that @%s at the lead's prompt, "+
		"or Claude there, takes their words now.", phase, why, phase)
}

// BoardWrite says on the board how the human writes to a phase agent, and what that does.
var BoardWrite = []string{
	"@<phase> <your words> at the lead's prompt writes to its agent, which gets them at its next tool call (an idle one wakes and goes on); " +
		"a phase with no agent at work, a done one among them, gets them through the lead",
	"an agent's own view (Claude Code's agent panel) wakes it, whatever it is; baton refuses one it let go every tool call",
}

// RelayHandle says, in a phase agent's question to the human, how they write to it.
func RelayHandle(phase string) string {
	return " To write to the agent, use " + Handle(phase) + " at the lead's prompt."
}
