package decide

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's question reaches the human in Claude Code's own dialog, which baton's mod opens in the
// lead's terminal (state.Relay). Its third option is what baton picks when nobody answers, as for the
// lead's own questions: a proposal goes ahead at the time its question names, or is answered Not now
// then if it is outward, a block is lifted then for the agent to look again (BlockLook), and anything
// else waits for the human, who has it again with /baton run, so that nothing waits on an agent's
// question on screen. These are the words the human and the agent read about all that.

// RelayOptions are the answers to phase agent question r.
func RelayOptions(r state.Relay) []Option {
	if r.AboutAgent() {
		return agentOptions(r)
	}
	pause := Option{"Pause baton", "I'm taking over; baton stops driving the plan"}
	wait := Option{"Wait for me", "Leave " + r.Phase + " waiting; /baton run asks me again"}
	goAhead := Option{"Go ahead", "Do what it proposed"}
	switch {
	case r.Timed() && r.Outward != "":
		return []Option{{"Wait for me", "Don't do it yet; /baton run asks me again, or it is answered Not now after " + Spell(HoldLapse) + " with no word"}, pause, {state.NotNow, notNowOption}, goAhead}
	case r.Timed():
		return []Option{{"Wait for me", "Don't do it yet; /baton run asks me again, or it goes ahead after " + Spell(HoldLapse) + " with no word"}, pause, goAhead}
	case r.Kind == state.RelayReview:
		return []Option{{"Continue", "I've seen them; let " + r.Phase + " go on"}, pause, wait}
	}
	return []Option{{"Continue", "I've dealt with it; " + r.Phase + " carries on"}, pause, {LookAgain, "The agent looks for a safe way around it and proposes it first"}}
}

// What an answer to a phase agent's question does (RelayOutcome).
const (
	OutcomeGo    = "go"    // its proposal goes ahead
	OutcomeLeft  = "left"  // its proposal was answered Not now: done only if safe, otherwise left for the human
	OutcomeOn    = "on"    // its block or its review is over, and its phase goes on
	OutcomeLook  = "look"  // its block is lifted, and its agent looks again for a safe way on
	OutcomeWait  = "wait"  // its phase waits for the human
	OutcomePause = "pause" // the run is paused, and its phase waits for the human
	OutcomeWords = "words" // the human answered in their own words, which the agent follows
)

// RelayOutcome is what answer does to phase agent question r. No answer (a question that went away
// unanswered) leaves it for the human, as Wait for me does.
func RelayOutcome(r state.Relay, answer string) string {
	switch {
	case answer == "" || answer == "Wait for me":
		return OutcomeWait
	case answer == "Pause baton":
		return OutcomePause
	case answer == "Go ahead" && r.Kind == state.RelayProposal:
		return OutcomeGo
	case answer == state.NotNow && r.Kind == state.RelayProposal:
		return OutcomeLeft
	case answer == "Continue" && r.Kind != state.RelayProposal:
		return OutcomeOn
	case answer == LookAgain && r.Kind == state.RelayBlocked:
		return OutcomeLook
	}
	return OutcomeWords
}

// RelayDefault is the answer baton picks for phase agent question r when nobody answers.
func RelayDefault(r state.Relay) string {
	if r.AboutAgent() {
		return agentDefault(r)
	}
	return RelayOptions(r)[2].Label
}

// RelayQuestion is the question that puts phase agent question r to the human. A proposal goes ahead at
// deadline, or is answered Not now then if it is outward, and a block is lifted then for the agent to
// look again.
func RelayQuestion(r state.Relay, deadline time.Time) Question {
	if r.AboutAgent() {
		return Question{Text: Sanitize(agentQuestion(r, deadline)), Options: RelayOptions(r)}
	}
	who := "baton: the agent on " + r.Phase
	var text string
	switch {
	case r.Timed() && r.Outward != "":
		text = outwardText(who, r.Because, r.What, r.Outward, deadline)
	case r.Timed():
		text = fmt.Sprintf("%s: %s. Unless you answer by %s, I will %s.", who, clause(r.Because), deadline.Local().Format("15:04"), lowerFirst(clause(r.What)))
	case r.Kind == state.RelayReview:
		text = fmt.Sprintf("%s has made %d decisions without you, as many as it may before you review them; /baton status lists each with its undo. Let %s go on?",
			who, r.Count, r.Phase)
	default:
		text = fmt.Sprintf("%s is blocked: %s. Only %s waits for you; the rest of the run goes on. Unless you answer by %s, it looks again for a safe way around it.",
			who, clause(r.Because), r.Phase, deadline.Local().Format("15:04"))
	}
	return Question{Text: Sanitize(text), Options: RelayOptions(r)}
}

// RelayGoAhead tells the agent on phase that the human chose Go ahead on its proposal id, to do what.
func RelayGoAhead(id, what, phase string) string {
	return fmt.Sprintf("[baton] The human chose Go ahead on your proposal %s. Do what you proposed now: %s. Then carry on with %s.", id, what, phase)
}

// RelayWentAhead tells the agent on phase that nobody answered its proposal id by deadline, so baton chose
// Go ahead: it does what, which undo reverses. last says it was the last decision the phase may make
// without the human before they review them.
func RelayWentAhead(id, what, undo, phase string, deadline time.Time, last bool) string {
	s := fmt.Sprintf("[baton] Nobody answered your proposal %s by %s, so baton chose Go ahead for the human. Do what you proposed now: %s. "+
		"If they later ask, it is undone with: %s.", id, deadline.Local().Format("15:04"), what, undo)
	if last {
		return s + " That was the last decision baton lets " + phase + " make without the human: once it is done, end your turn, and baton stops " +
			phase + " for them to review the decisions."
	}
	return s + " Then carry on with " + phase + "."
}

// RelayNotNow is what baton says to the human (say) and tells the agent on phase (tell) when its outward
// proposal id, to do what, was answered Not now: by baton, as nobody answered by deadline, or by the human
// (a zero deadline).
func RelayNotNow(id, what, phase string, deadline time.Time) (say, tell string) {
	return notNow(phase, id, what, phase, deadline)
}

// RelayHeld tells the agent on phase not to do what its proposal id proposed (what): why says what became
// of its question. It waits for the human, and baton gives it their answer.
func RelayHeld(id, what, phase, why string) string {
	return fmt.Sprintf("[baton] %s: do not do what you proposed in %s (%s). End your turn now: %s waits for the human, "+
		"and baton gives you their answer here.", why, id, what, phase)
}

// RelayContinue tells the agent on phase that the human chose Continue on its block (because) or its
// review (kind).
func RelayContinue(kind, because, phase string) string {
	if kind == state.RelayReview {
		return fmt.Sprintf("[baton] The human reviewed the decisions %s made without them and chose Continue. Carry on with %s.", phase, phase)
	}
	return fmt.Sprintf("[baton] The human chose Continue: they have dealt with what blocked you (%s). Carry on with %s.", because, phase)
}

// RelayOwnWords gives the agent on phase the human's own answer to its question r.
func RelayOwnWords(r state.Relay, answer, phase string) string {
	switch r.Kind {
	case state.RelayProposal:
		return fmt.Sprintf("[baton] The human answered your proposal %s (%s) in their own words: %s. That is their instruction, not a Go ahead: "+
			"do what it says, and do what you proposed only if it says so. Then carry on with %s.", r.Decision, r.What, strconv.Quote(answer), phase)
	case state.RelayReview:
		return fmt.Sprintf("[baton] The human answered the review of the decisions %s made without them in their own words: %s. "+
			"That is their instruction: follow it, then carry on with %s.", phase, strconv.Quote(answer), phase)
	}
	return fmt.Sprintf("[baton] The human answered your block (%s) in their own words: %s. That is their instruction: follow it, then carry on with %s.",
		r.Because, strconv.Quote(answer), phase)
}

// RelayWaits is the notice that the phase agent on phase waits for the human (what): nobody answered its
// question with a way on.
func RelayWaits(phase, what string) string {
	return fmt.Sprintf("the agent on %s waits for you (%s): /baton run asks you again", phase, what)
}

// RelayWithdrawn tells the human that the question of the agent on phase needs no answer any more (why).
func RelayWithdrawn(phase, why string) string {
	return fmt.Sprintf("baton: the question of the agent on %s needs no answer any more: %s", phase, why)
}

// RelayPaused tells the human that baton is paused, as they chose on the question of the agent on phase.
func RelayPaused(phase string) string {
	return "baton: paused — the human is driving; the agent on " + phase + " waits for you, and /baton run asks you again"
}

// RelayAgain tells the human, through the lead's model, which reads it first, that /baton run puts the
// question of the agent on phase to them again: nothing of it is the model's.
func RelayAgain(phase string) string {
	return "baton: the agent on " + phase + " waits for the human, and baton's mod asks them its question again; nothing of it is yours to do"
}
