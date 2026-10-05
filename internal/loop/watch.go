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
	if l.lastTick.IsZero() || v.Now.Sub(l.lastTick) > l.Timing.ClockJump {
		// First tick, or the machine slept: idle timers restart from now.
		if !l.lastTick.IsZero() {
			l.logf("loop: clock jumped %s (sleep?); idle timers restart", v.Now.Sub(l.lastTick).Round(time.Second))
		}
		l.wokeAt = v.Now
	}
	l.lastTick = v.Now

	l.watchDialog(v, st)
	if st.Run.Escalation != nil || st.Blocked != nil || st.Run.TurnOpen || st.Run.Dialog != nil {
		l.idleNudged = time.Time{} // the human has it, or the model is working: nothing is stalled
		return
	}
	comp := st.Run.Compaction
	if comp.InFlight() || comp.Status == state.CompactDone && comp.ByBaton && !st.Run.TurnStarted.After(comp.Finished) {
		return // the compaction checks own this stretch
	}
	if e := st.Run.LastError; e != nil && !st.Run.TurnStarted.After(e.At) {
		l.watchError(v, st, in, *e)
		return
	}
	l.errRetries = 0

	quietSince := latest(st.Run.LastActivity, st.Run.LastStop, l.wokeAt, l.idleNudged)
	if busy := st.Run.BusyBackground(); len(busy) > 0 && v.Now.Sub(st.Run.LastStop) < l.Timing.BackgroundMax {
		return // background work will wake the session when it finishes
	}
	if w := st.Waiting; w != nil && v.Now.Before(w.Until.Add(l.Timing.WaitGrace)) {
		return
	}
	if v.Now.Sub(quietSince) < l.Timing.IdleNudge {
		return
	}
	if !l.idleNudged.IsZero() {
		// Already reminded once and still nothing: bring the human in.
		l.escalate("stalled", fmt.Sprintf("no activity on %s for %s, even after a reminder", st.Current, v.Now.Sub(st.Run.LastActivity).Round(time.Minute)))
		return
	}
	if l.gate(v, st) != "" {
		return
	}
	msg := l.idleMessage(v, st)
	l.idleNudged = v.Now
	l.Store.Event("idle_nudge", map[string]any{"phase": st.Current, "quiet_for": v.Now.Sub(quietSince).Round(time.Second).String()})
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
func (l *Loop) watchDialog(v host.View, st state.State) {
	d := st.Run.Dialog
	if d == nil || v.Now.Sub(d.Since) < l.Timing.DialogNotify || l.dialogNotified.Equal(d.Since) {
		return
	}
	l.dialogNotified = d.Since
	l.update(func(st *state.State) {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "dialog", Text: "waiting for your answer (" + d.Tool + ")", At: v.Now})
	})
	l.Store.Event("dialog_waiting", map[string]any{"tool": d.Tool})
}

// watchError resumes a turn that an API error ended. A usage limit is announced once and retried
// slowly; overloads and server errors are retried with backoff. A turn that starts on its own (Claude
// Code may resume by itself) supersedes all of this.
func (l *Loop) watchError(v host.View, st state.State, in host.Injector, e state.StopError) {
	base := l.Timing.OverloadRetry
	if e.Error == "rate_limit" {
		base = l.Timing.RateLimitRetry
		if !l.rateNotified.Equal(e.At) {
			l.rateNotified = e.At
			l.update(func(st *state.State) {
				st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "rate_limit", Text: e.Details, At: v.Now})
			})
		}
	}
	wait := base << min(l.errRetries, 4) // back off: ×1, ×2, ×4, ×8, ×16
	since := latest(e.At, l.errNudged)
	if v.Now.Sub(since) < wait || l.gate(v, st) != "" {
		return
	}
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
