package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Recovered is what the model is told when baton brought the lead back after claude exited by itself
// mid-plan (why: "claude exited with code 137"), docs/research/recovery.md. In a resumed conversation it
// wakes the model: the turn under way was lost, and phase goes on from what is already done. In a fresh
// session (fresh: the conversation could not be resumed) it is the session's first prompt, under the
// brief the session started with; phase is "" when no phase is in hand, and the context above says
// where the plan stands.
func (w Words) Recovered(phase, why string, fresh bool) string {
	what := "Continue " + phase
	if phase == "" {
		what = "Continue the plan"
	}
	if fresh {
		if phase == "" {
			return fmt.Sprintf("[baton] %s mid-plan and its conversation could not be resumed, so baton started this fresh session: the context above says where the plan stands. Go on from there.", why)
		}
		return fmt.Sprintf("[baton] %s mid-plan and its conversation could not be resumed, so baton started this fresh session: the brief above has %s's instructions. %s from there.", why, phase, what)
	}
	return fmt.Sprintf("[baton] %s mid-plan, and baton resumed this conversation: the turn that was under way was lost. "+
		"%s: some of it may be done already, so check what is (%s) before you redo anything, then go on from there.", why, what, w.doneCheck())
}

// doneCheck is how the model finds out what a phase has done already, when the conversation does not say.
func (w Words) doneCheck() string {
	if w.Git {
		return "`git log`, `git status`, and the notes the phase left"
	}
	return "the files the phase is about, and the notes it left"
}

// RecoveryBackoff is the notice when claude exited by itself mid-plan more often than baton brings it back
// at once: times within window, the last time for why. The host stays, and brings it back at at, then every
// every while it keeps exiting; the human may stop it in baton's terminal and resume the conversation
// (session) themselves.
func RecoveryBackoff(times int, window time.Duration, why string, at time.Time, every time.Duration, session string) string {
	return fmt.Sprintf("claude exited unexpectedly %d times in %s (the last: %s), so baton waits before it brings it back: at %s, and every %s after that while it keeps exiting. "+
		"To bring it back yourself, press Ctrl-C in baton's terminal and run `baton --resume %s`.", times, Spell(window), why, at.Local().Format("15:04"), Spell(every), session)
}

// RecoveryStopped is the escalation when the human stopped baton from bringing claude back, during its wait
// (Ctrl-C in its terminal): the conversation (session) is theirs to resume.
func RecoveryStopped(session string) string {
	return fmt.Sprintf("you stopped baton bringing claude back. To go on, run `baton --resume %s`.", session)
}

// ResumedQuietly is the line for the human when baton resumed a conversation whose last turn stands refused
// (why: how claude exited): its model is left asleep, since nothing is typed into a refused conversation,
// and how it goes on stays theirs (Refused).
func ResumedQuietly(why string) string {
	return fmt.Sprintf("baton: %s mid-plan → resumed this conversation; its last turn stands refused, so baton leaves it to you: rewind (Esc Esc), or /clear for a fresh one", why)
}

// FailedTurn says how a turn that ended in API error e failed, for the human and the model: "the last turn
// failed with an API error (invalid_request: prompt is too long: 213456 tokens > 200000 maximum)".
func FailedTurn(e state.StopError) string {
	detail := e.Details
	if detail == "" {
		detail = e.Message
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if r := []rune(detail); len(r) > 200 {
		detail = string(r[:199]) + "…"
	}
	if detail == "" {
		return fmt.Sprintf("the last turn failed with an API error (%s)", e.Error)
	}
	return fmt.Sprintf("the last turn failed with an API error (%s: %s)", e.Error, detail)
}

// Afresh is the first prompt of the fresh session baton started for the lead after an API error a fresh
// session avoids (why: FailedTurn): the API refused the conversation itself, so it could not go on. Under
// the brief the session started with, it says to continue phase ("" with no phase in hand: the context
// above says where the plan stands).
func (w Words) Afresh(phase, why string) string {
	if phase == "" {
		return fmt.Sprintf("[baton] In the conversation before this one, %s, which every turn of it would have met again, so baton started this fresh session: "+
			"the context above says where the plan stands. Go on from there.", why)
	}
	return fmt.Sprintf("[baton] In the conversation before this one, %s, which every turn of it would have met again, so baton started this fresh session: "+
		"the brief above has %s's instructions. Continue %s from there: some of it may be done already, so check what is (%s) before you redo anything.",
		why, phase, phase, w.doneCheck())
}

// AfreshLine is the line for the human when baton started the lead afresh after an API error a fresh session
// avoids (why: FailedTurn), from phase's brief ("" with no phase in hand).
func AfreshLine(why, phase string) string {
	from := "for the run"
	if phase != "" {
		from = "from " + phase + "'s brief"
	}
	return fmt.Sprintf("baton: %s, which a fresh session avoids → started a fresh session %s", why, from)
}

// AgentFailedTurn is why a phase agent's phase is taken up by a fresh agent after its last turn failed with
// an API error a fresh agent avoids (why: FailedTurn): for the human, and the roster.
func AgentFailedTurn(why string) string {
	return "its agent's " + strings.TrimPrefix(why, "the ") + ", which a fresh agent avoids"
}

// AfreshLater is the notice when baton waits before it starts the session stopped as s describes afresh,
// at at, after an API error a fresh session avoids (why: FailedTurn): it has started fresh ones often in
// the last window already.
func AfreshLater(s Stop, why string, at time.Time, window time.Duration) string {
	who, fresh := "the lead session", "session"
	switch {
	case s.Worker:
		who, fresh = "the phase agent on "+s.Phase, "agent"
	case s.Phase != "":
		who = s.Phase
	}
	return fmt.Sprintf("%s: %s once more, which a fresh %s avoids. baton has started fresh ones often in the last %s, so it starts the next at %s.",
		who, why, fresh, Spell(window), at.Local().Format("15:04"))
}
