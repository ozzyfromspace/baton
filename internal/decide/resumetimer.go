package decide

import (
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The resume timer's words (state/resume.go, loop/usage.go): after a usage limit whose reset is known, a
// run working on its own goes on as soon as the window resets, unless the human pauses or writes.

// UsageWait is the announcement of the run's resume timer, armed to fire at resume as the session who
// ("this session", "the worker on P3") hit a limit of window that resets at resets: "baton: the worker on
// P3 hit the account's weekly usage limit, until Mon 10:00. baton resumes the run at 10:02 unless you pause
// or write." With the reset unknown (zero), the limit held again just after the timer fired, for the
// try'th time.
func UsageWait(who, window string, resets, resume, now time.Time, try int) string {
	if resets.IsZero() {
		return "baton: " + who + " met the account's usage limit again just after it reset (try " + strconv.Itoa(try) +
			"). baton tries again at " + Clock(resume, now) + " unless you pause or write."
	}
	return UsageHit(who, window, resets, now) + " baton resumes the run at " + Clock(resume, resets) + " unless you pause or write."
}

// UsageHit says that the session who hit the account's usage limit of window, which resets at resets.
func UsageHit(who, window string, resets, now time.Time) string {
	return "baton: " + who + " hit the account's " + windowName(window) + ", until " + Clock(resets, now) + "."
}

// UsageReset is a session's retry as the timer fires: its turn ended on the usage limit, which has reset.
// Its phase is phase ("": none in hand).
func UsageReset(phase string) string {
	if phase == "" {
		return "[baton] The usage limit has reset. Carry on."
	}
	return "[baton] The usage limit has reset. Continue " + phase + "."
}

// UsageResumed is the announcement of the timer firing: the run's sessions it woke, as the human reads them
// ("this session", "the worker on P3"; none: the run's other work goes on).
func UsageResumed(woke []string) string {
	if len(woke) == 0 {
		return "baton: the usage limit has reset, and the run goes on."
	}
	return "baton: the usage limit has reset: baton resumed " + strings.Join(woke, ", ") + "."
}

// ResumeCancelled is the announcement that the resume timer was cancelled, by the human's pause or by a
// prompt of theirs (state.CancelPause, state.CancelPrompt), which paused the run: one they wrote while it
// waited, or, atHit, the one whose turn hit the limit.
func ResumeCancelled(by string, atHit bool) string {
	why := "you paused baton"
	switch {
	case by == state.CancelPrompt && atHit:
		why = "the turn that hit it was yours, so baton paused the run"
	case by == state.CancelPrompt:
		why = "you wrote while it waited, so baton paused the run"
	}
	return "baton: auto-resume after the usage limit cancelled (" + why + "): baton will not resume by itself; /baton run resumes."
}

// ResumeCancelledTell is what the lead's model is told beside the human's prompt that cancelled the timer.
const ResumeCancelledTell = "[baton] The human wrote while the run waited for the account's usage limit to reset, so baton paused " +
	"the run and will not resume it by itself. Answer the human; the plan goes on when they run /baton run."

// StatusResume is what baton status says of the resume timer after its usage limit line: when it resumes
// the run (resumes), or who cancelled it (state.CancelPause, …); "" for no timer.
func StatusResume(resumes time.Time, cancelled string, now time.Time) string {
	switch {
	case !resumes.IsZero():
		return " · baton resumes the run at " + Clock(resumes, now) + " unless you pause or write"
	case cancelled != "":
		return " · auto-resume cancelled (" + cancelled + "): /baton run resumes"
	}
	return ""
}
