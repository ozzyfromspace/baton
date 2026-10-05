package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The watchdog catches a run that has gone quiet. "Working" is judged from hook activity, never from
// running processes, so a leftover shell or monitor cannot make a stalled session look busy. Every
// reminder goes through the same typing gates as /compact, so it never lands on a human's draft or
// in an open dialog.

// watch runs after the compaction checks on every tick.
func (l *Loop) watch(v host.View, st state.State, in host.Injector) {
	l.watchDialog(v, st)
	l.answerWarning(v, st, in)
	comp := st.Run.Compaction
	if comp.InFlight() || comp.Status == state.CompactFailed || comp.Status == state.CompactDone && comp.ByBaton && !st.Run.TurnStarted.After(comp.Finished) {
		return // the compaction checks own this stretch
	}
	// An API error that ended the last turn is retried even while an older escalation is open: the
	// escalation is about something else, and the error would otherwise leave the run parked.
	if e := st.Run.LastError; e != nil && !st.Run.TurnStarted.After(e.At) {
		if esc := st.Run.Escalation; esc == nil || esc.Kind == "api_error" || e.At.After(esc.Since) {
			l.watchError(v, st, in, *e)
			return
		}
	}
	l.errRetries, l.rateNotified = 0, false

	if st.Run.Escalation != nil || st.Blocked != nil || l.why == gateTurn || st.Run.Dialogs.AnyOpen() {
		l.idleNudged = time.Time{} // the human has it, or the model is working: nothing is stalled
		return
	}
	if !l.idleNudged.IsZero() && st.Run.LastActivity.After(l.idleNudged) {
		l.idleNudged = time.Time{} // the model answered the reminder (IdleNudges still counts it)
	}
	quietSince := latest(st.Run.LastActivity, st.Run.LastStop, l.wokeAt, l.idleNudged)
	if busy := st.Run.BusyBackground(); len(busy) > 0 && v.Now.Sub(l.since(st.Run.LastStop)) < l.Timing.BackgroundMax {
		return // background work will wake the session when it finishes
	}
	if w := st.Waiting; w != nil && v.Now.Before(w.Until.Add(l.Timing.WaitGrace)) {
		return
	}
	if v.Now.Sub(quietSince) < l.Timing.IdleNudge {
		return
	}
	switch {
	case !l.idleNudged.IsZero():
		// Already reminded once and still nothing: bring the human in.
		l.escalate("stalled", fmt.Sprintf("no activity on %s for %s, even after a reminder", st.Current, v.Now.Sub(st.Run.LastActivity).Round(time.Minute)))
		return
	case st.Run.IdleNudges >= MaxIdleNudges:
		// The model answers reminders but the plan does not move (a wait declared again and again, or a
		// stop that background work let through): more reminders would only burn tokens.
		l.escalate("stalled", fmt.Sprintf("%s has not moved after %d reminders", st.Current, st.Run.IdleNudges))
		return
	}
	if l.why != "" {
		l.held(v, st, "remind the idle model")
		return
	}
	l.proceed()
	msg := l.idleMessage(v, st)
	l.idleNudged = v.Now
	l.update(func(st *state.State) { st.Run.IdleNudges++ })
	l.Store.Event("idle_nudge", map[string]any{"phase": st.Current, "quiet_for": v.Now.Sub(quietSince).Round(time.Second).String(), "count": st.Run.IdleNudges + 1})
	in.Type(msg, true)
}

func (l *Loop) idleMessage(v host.View, st state.State) string {
	switch {
	case st.Waiting != nil:
		return fmt.Sprintf("[baton] Your declared wait for %q has run out. Check on it, then continue %s, or report your status.", st.Waiting.What, st.Current)
	case len(st.Run.BusyBackground()) > 0:
		return fmt.Sprintf("[baton] Background work has been running for over %s with no progress on %s. Check on it, then continue, or report your status.", l.Timing.BackgroundMax, st.Current)
	default:
		return fmt.Sprintf("[baton] No activity for %s and %s is not reported done. Continue it, or report: baton done / baton blocked / baton waiting.", l.Timing.IdleNudge, st.Current)
	}
}

// watchDialog tells the human when Claude Code has been waiting on them (a permission prompt or a
// question) for a while. It is a notice, not an escalation: the session is fine, it needs an answer.
// It is repeated on the same schedule as escalation reminders, since the run waits until it is answered.
func (l *Loop) watchDialog(v host.View, st state.State) {
	d, waiting := st.Run.Dialogs.Front()
	if !waiting {
		return
	}
	due := []time.Duration{l.Timing.DialogNotify}
	for _, r := range Reminders {
		due = append(due, l.Timing.DialogNotify+r)
	}
	open := v.Now.Sub(l.since(d.Since))
	n := 0
	for n < len(due) && open >= due[n] {
		n++
	}
	if n == 0 || !l.dialogNotified.IsZero() && !l.dialogNotified.Before(d.Since.Add(due[n-1])) {
		return
	}
	l.dialogNotified = d.Since.Add(due[n-1])
	l.update(func(st *state.State) {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "dialog", Text: "waiting for your answer (" + d.Tool + ")", At: v.Now})
	})
	l.Store.Event("dialog_waiting", map[string]any{"tool": d.Tool, "count": n})
}

// answerWarning answers baton's own context question when nobody has for WarnTimeout. The question
// only warns: Claude Code compacts on its own when the context is full, so an unattended run should not
// stop on it. baton picks the second option, "Keep going", by its number, which selects it wherever the
// highlight is (docs/research/reliability.md); the answer then arrives through the usual hook. It never
// types while the human is typing or composing an answer, and only once per question.
func (l *Loop) answerWarning(v host.View, st state.State, in host.Injector) {
	d, alone := st.Run.Dialogs.Only()
	if !alone || d.Kind != "context_warning" || !d.AutoAnswered.IsZero() || v.Now.Sub(l.since(d.Since)) < l.Timing.WarnTimeout {
		return
	}
	if v.Draft || v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff {
		return
	}
	l.update(func(st *state.State) {
		for i := range st.Run.Dialogs {
			if st.Run.Dialogs[i].Same(d) {
				st.Run.Dialogs[i].AutoAnswered = v.Now
			}
		}
	})
	l.Store.Event("warning_timed_out", map[string]any{"after": l.Timing.WarnTimeout.String()})
	in.Type("2", false)
}

// transientErrors are API errors worth retrying: they pass on their own. Anything else (an expired
// login, billing, a model that does not exist, a request Claude Code refuses to send) needs the human.
var transientErrors = map[string]bool{"rate_limit": true, "overloaded": true, "server_error": true, "unknown": true, "max_output_tokens": true}

// MaxErrorRetries is how many transient-error retries happen before the human is told (retries go on).
const MaxErrorRetries = 6

// watchError resumes a turn that an API error ended. A usage limit is announced once per episode and
// retried every 15-30 minutes, so the run resumes soon after the limit resets. Overloads and server
// errors are retried with backoff, and reported if they persist. Errors that will not pass on their
// own are reported at once and not retried. A turn that starts on its own (Claude Code may resume by
// itself) supersedes all of this.
func (l *Loop) watchError(v host.View, st state.State, in host.Injector, e state.StopError) {
	if !transientErrors[e.Error] {
		if esc := st.Run.Escalation; esc == nil || esc.Kind != "api_error" {
			reason := fmt.Sprintf("the last turn failed with an API error baton cannot retry away (%s)", e.Error)
			if e.Details != "" {
				reason += ": " + e.Details
			}
			l.escalate("api_error", reason)
		}
		return
	}
	var wait time.Duration
	if e.Error == "rate_limit" {
		if !l.rateNotified {
			l.rateNotified = true
			l.update(func(st *state.State) {
				st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "rate_limit", Text: e.Details, At: v.Now})
			})
		}
		wait = l.Timing.RateLimitRetry << min(l.errRetries, 1) // ×1, then ×2: limits reset at unknown times
	} else {
		wait = l.Timing.OverloadRetry << min(l.errRetries, 4) // back off: ×1, ×2, ×4, ×8, ×16
		if l.errRetries >= MaxErrorRetries {
			if esc := st.Run.Escalation; esc == nil || esc.Kind != "api_error" {
				l.escalate("api_error", fmt.Sprintf("the API keeps failing (%s) after %d retries; baton keeps retrying", e.Error, l.errRetries))
			}
		}
	}
	if v.Now.Sub(l.since(latest(e.At, l.errNudged))) < wait {
		return
	}
	if l.why != "" {
		l.held(v, st, "retry after an API error")
		return
	}
	l.proceed()
	l.errRetries++
	l.errNudged = v.Now
	l.Store.Event("error_retry", map[string]any{"error": e.Error, "retry": l.errRetries})
	in.Type(fmt.Sprintf("[baton] The last turn ended with an API error (%s). Continue %s.", e.Error, st.Current), true)
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}
