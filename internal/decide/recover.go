package decide

import (
	"fmt"
	"time"
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

// RecoveryCapped is the escalation when claude exited by itself mid-plan once more than baton resumes it:
// times within window. baton stops resuming it, and the human resumes the conversation (session).
func RecoveryCapped(times int, window time.Duration, session string) string {
	return fmt.Sprintf("claude exited unexpectedly %d times in %s, so baton stopped resuming it. To go on, run `baton --resume %s`.", times, Spell(window), session)
}

// FreshSpent is the escalation when claude could not start again for a phase that has had its fresh session
// already (why: how it exited, before the session started): baton stops trying, and the human resumes the
// conversation (session) once claude starts again.
func FreshSpent(phase, why, session string) string {
	what := "the plan"
	if phase != "" {
		what = phase
	}
	return fmt.Sprintf("%s before its session started, and baton had started a fresh session for %s already, so it stopped trying. "+
		"Once claude starts again, run `baton --resume %s`.", why, what, session)
}
