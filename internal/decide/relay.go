package decide

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A worker's question reaches the human through the lead session, which puts it to them in their
// terminal (state.Relay). Its third option is what baton picks when nobody answers, as for the lead's own
// questions (its key, 3, is "No" in a permission prompt): a timed proposal goes ahead at the time its
// question names, and anything else waits for the human in the worker's own session (baton view), so
// that the lead never waits on a worker's question. These are the words the lead and the worker read
// about all that.

// RelayOptions are the answers to worker question r.
func RelayOptions(r state.Relay) []Option {
	pause := Option{"Pause baton", "I'm taking over; baton stops driving the plan"}
	wait := Option{"Wait for me", fmt.Sprintf("Leave %s waiting; I'll answer in its session (baton view %s)", r.Phase, r.Phase)}
	goAhead := Option{"Go ahead", "Do what it proposed"}
	switch {
	case r.Timed():
		return []Option{{"Wait for me", fmt.Sprintf("Don't do it yet; I'll answer in its session (baton view %s)", r.Phase)}, pause, goAhead}
	case r.Kind == state.RelayProposal:
		return []Option{goAhead, pause, wait}
	case r.Kind == state.RelayReview:
		return []Option{{"Continue", "I've seen them; let " + r.Phase + " go on"}, pause, wait}
	}
	return []Option{{"Continue", "I've dealt with it; " + r.Phase + " carries on"}, pause, wait}
}

// What an answer to a worker's question does (RelayOutcome).
const (
	OutcomeGo    = "go"    // its proposal goes ahead
	OutcomeOn    = "on"    // its block or its review is over, and its phase goes on
	OutcomeWait  = "wait"  // it waits for the human in the worker's own session
	OutcomePause = "pause" // the run is paused, and it waits for the human in the worker's own session
	OutcomeWords = "words" // the human answered in their own words, which the worker follows
)

// RelayOutcome is what answer does to worker question r. No answer (a question that went away
// unanswered) leaves it for the human, as Wait for me does.
func RelayOutcome(r state.Relay, answer string) string {
	switch {
	case answer == "" || answer == "Wait for me":
		return OutcomeWait
	case answer == "Pause baton":
		return OutcomePause
	case answer == "Go ahead" && r.Kind == state.RelayProposal:
		return OutcomeGo
	case answer == "Continue" && r.Kind != state.RelayProposal:
		return OutcomeOn
	}
	return OutcomeWords
}

// RelayDefault is the answer baton picks for worker question r when nobody answers.
func RelayDefault(r state.Relay) string { return RelayOptions(r)[2].Label }

// RelayQuestion is the question that puts worker question r to the human, from the lead session. A timed
// proposal goes ahead at deadline.
func RelayQuestion(r state.Relay, deadline time.Time) Question {
	who := "baton: worker " + r.Phase
	var text string
	switch {
	case r.Timed():
		text = fmt.Sprintf("%s: %s. Unless you answer by %s, I will %s.", who, clause(r.Because), deadline.Local().Format("15:04"), lowerFirst(clause(r.What)))
	case r.Kind == state.RelayProposal:
		text = fmt.Sprintf("%s: %s. I propose to %s, and will not do it without you (%s).", who, clause(r.Because), lowerFirst(clause(r.What)), r.Untimed)
	case r.Kind == state.RelayReview:
		text = fmt.Sprintf("%s has made %d decisions without you, as many as it may before you review them; /baton status lists each with its undo. Let %s go on?",
			who, r.Count, r.Phase)
	default:
		text = fmt.Sprintf("%s is blocked: %s. Only %s waits for you; the rest of the run goes on.", who, clause(r.Because), r.Phase)
	}
	return Question{Text: Sanitize(text), Options: RelayOptions(r)}
}

// RelayAsk tells the lead's model to put the question of the worker on phase to the human now; call is
// its exact call.
func RelayAsk(phase, call string) string {
	return fmt.Sprintf("[baton] The worker on %s has a question for the human, and only this session can put it to them. Before anything else, %s. "+
		"baton gives the answer to the worker itself; then carry on with your own work.", phase, call)
}

// RelayStopped refuses the lead's stop: it has not put the question of the worker on phase to the human.
func RelayStopped(phase, call string) string {
	return fmt.Sprintf("[baton] You stopped, but put the question of the worker on %s to the human first: %s. "+
		"baton gives the answer to the worker itself; then carry on with your own work.", phase, call)
}

// RelayHold holds the lead's tool call until it has put the question of the worker on phase to the human.
func RelayHold(phase, call string) string {
	return fmt.Sprintf("[baton] Not yet: put the question of the worker on %s to the human first: %s. Until they have it, nothing else runs.", phase, call)
}

// RelayTyped is the line typed into the idle lead session to have it put the question of the worker on
// phase to the human; the exact call comes with the prompt.
func RelayTyped(phase string) string { return "[baton] worker " + phase + " has a question for you" }

// RelayPassed tells the lead's model that the human answered the question of the worker on phase, or
// that nobody did and baton chose answer for them (auto). cur is the lead's phase in hand ("" for none).
func RelayPassed(phase, answer string, auto bool, cur string) string {
	who := "The human answered the question of the worker on " + phase
	if auto {
		who = "Nobody answered the question of the worker on " + phase + ", so baton chose " + answer + " for the human"
	}
	next := "Carry on with what you were doing."
	if cur != "" {
		next = "Carry on with " + cur + "."
	}
	return fmt.Sprintf("[baton] %s. baton gives the answer to the worker itself: nothing about it is yours to do. %s", who, next)
}

// RelaySettled tells the lead's model that the question of the worker on phase it put to the human was
// settled already, in the worker's own session or because the worker is gone: the answer changed nothing.
func RelaySettled(phase, cur string) string {
	next := "Carry on with what you were doing."
	if cur != "" {
		next = "Carry on with " + cur + "."
	}
	return fmt.Sprintf("[baton] That question of the worker on %s was settled already, so the answer changed nothing, and nothing about it is yours to do. %s", phase, next)
}

// RelayPaused tells the lead's model that the human chose Pause baton on a worker's question.
func RelayPaused() string {
	return "[baton] The human chose Pause baton: baton is paused. Wait for the human's instructions."
}

// RelayGoAhead tells a worker on phase that the human chose Go ahead on its proposal id, to do what.
func RelayGoAhead(id, what, phase string) string {
	return fmt.Sprintf("[baton] The human chose Go ahead on your proposal %s. Do what you proposed now: %s. Then carry on with %s.", id, what, phase)
}

// RelayWentAhead tells a worker on phase that nobody answered its proposal id by deadline, so baton chose
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

// RelayHeld tells a worker on phase not to do what its proposal id proposed (what): why says what became
// of its question. It waits for the human, who answers in the worker's own session.
func RelayHeld(id, what, phase, why string) string {
	return fmt.Sprintf("[baton] %s: do not do what you proposed in %s (%s). End your turn now: %s waits for the human, who answers in this session, "+
		"and their message reaches you here.", why, id, what, phase)
}

// RelayContinue tells a worker on phase that the human chose Continue on its block (because) or its
// review (kind).
func RelayContinue(kind, because, phase string) string {
	if kind == state.RelayReview {
		return fmt.Sprintf("[baton] The human reviewed the decisions %s made without them and chose Continue. Carry on with %s.", phase, phase)
	}
	return fmt.Sprintf("[baton] The human chose Continue: they have dealt with what blocked you (%s). Carry on with %s.", because, phase)
}

// RelayOwnWords gives a worker on phase the human's own answer to its question r.
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

// WorkerAnsweredHere tells a worker on phase that the human wrote in its own session, where its block
// (because) or its review (kind) waited for them: their message is the answer.
func WorkerAnsweredHere(kind, because, phase string) string {
	what := "your block (" + because + ")"
	if kind == state.RelayReview {
		what = "the review of the decisions " + phase + " made without them"
	}
	return fmt.Sprintf("[baton] The human has written to you in this session: their message answers %s. Follow it, then carry on with %s.", what, phase)
}
