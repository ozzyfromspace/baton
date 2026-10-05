// Package loop is the host's controller: on every tick it reads the project state the hooks keep, and
// it is the only part of baton that acts on its own. It types /compact when a compaction is queued and
// it is safe to type, demands proof the compaction happened, nudges a model that did not resume, and
// sends the human the notices the hooks queued.
//
// Nothing here may wait forever. Every state baton waits in has a deadline, and every gate that holds
// baton back either expires (a screen that never settles, a turn with no sign of life) or is reported
// to the human with its reason.
package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Timing is every deadline the loop enforces.
type Timing struct {
	Quiet          time.Duration // the screen must be still this long before baton types
	QuietMax       time.Duration // a screen that never settles (a status line redrawn every second) is ignored after this
	HandsOff       time.Duration // and the human must not have typed for this long
	AckTimeout     time.Duration // PreCompact must fire this soon after typing /compact
	CompactTimeout time.Duration // PostCompact must fire this soon after PreCompact
	FailedRetry    time.Duration // a failed compaction is tried again after this, then 3× longer each round
	ResumeNudge    time.Duration // no turn this long after a compaction: type a reminder
	ResumeEscalate time.Duration // still nothing this long after the reminder: bring the human in
	DraftEscalate  time.Duration // a human draft blocking baton this long: tell them
	StaleTurn      time.Duration // an open turn with no hook activity this long is not trusted to be open
	IdleNudge      time.Duration // no hook activity this long mid-plan: type a reminder (then escalate)
	WaitGrace      time.Duration // extra time after a declared wait runs out
	BackgroundMax  time.Duration // background work may hold a quiet session (or a compaction) this long
	DialogNotify   time.Duration // a permission prompt or question open this long: notify the human
	RateLimitRetry time.Duration // retry interval after a usage-limit error (doubled once at most)
	OverloadRetry  time.Duration // first retry after an overload or server error (then backoff)
	ClockJump      time.Duration // a gap between ticks this long means the machine slept
	NoticeRetry    time.Duration // a notice that failed to send is retried after this
}

// DefaultTiming is baton's production timing.
var DefaultTiming = Timing{
	Quiet: 1500 * time.Millisecond, QuietMax: 30 * time.Second, HandsOff: 3 * time.Second,
	AckTimeout: 15 * time.Second, CompactTimeout: 10 * time.Minute, FailedRetry: 5 * time.Minute,
	ResumeNudge: 60 * time.Second, ResumeEscalate: 3 * time.Minute, DraftEscalate: 2 * time.Minute,
	StaleTurn: 15 * time.Minute, IdleNudge: 10 * time.Minute, WaitGrace: time.Minute, BackgroundMax: 30 * time.Minute,
	DialogNotify: 3 * time.Minute, RateLimitRetry: 15 * time.Minute, OverloadRetry: time.Minute,
	ClockJump: 2 * time.Minute, NoticeRetry: time.Minute,
}

// MaxTries is how many times baton types /compact in one round before it gives up on the round.
const MaxTries = 2

// MaxRounds is how many rounds of attempts a failed compaction gets (5, 15 and 45 minutes apart).
const MaxRounds = 3

// MaxIdleNudges is how many reminders baton types without the plan moving before it brings the human in.
const MaxIdleNudges = 2

// Reminders are when an unresolved escalation is pushed again, measured from when it was raised.
var Reminders = []time.Duration{30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// clearLine is sent before retrying: Ctrl-U ×6 wipes whatever is left of baton's own failed attempt.
// It is only sent when the human has no draft (a gate), so it never erases their text.
const clearLine = "\x15\x15\x15\x15\x15\x15"

// Loop implements host.Controller.
type Loop struct {
	Store   *state.Store
	Notify  notify.Notifier
	Project string
	Timing  Timing
	Logf    func(string, ...any)

	why       string    // this tick's gate: why baton must not type now ("" if it may)
	openSince time.Time // since when every gate but the quiet screen has been open
	noisyLog  bool      // the never-quiet screen was reported
	staleLog  time.Time // the turn whose staleness was reported
	heldWhy   string    // what has been holding back something baton needs to type
	heldSince time.Time
	wished    bool // something wanted to type this tick

	lastTick       time.Time
	wokeAt         time.Time
	idleNudged     time.Time
	dialogNotified time.Time
	rateNotified   bool
	errNudged      time.Time
	errRetries     int
}

// Tick runs once per host tick.
func (l *Loop) Tick(v host.View, in host.Injector) {
	if !v.Owner {
		return
	}
	l.clock(v)
	st, err := l.Store.Load()
	if err != nil {
		l.logf("loop: %v", err)
		return
	}
	l.sendNotices(v, st)
	if st.Mode != state.ModeRunning {
		return
	}
	l.why = l.gate(v, st)
	l.wished = false
	l.resolve(v, st)
	l.remind(v, st)

	comp := st.Run.Compaction
	switch comp.Status {
	case state.CompactQueued:
		l.typeCompact(v, st, in)
	case state.CompactTyped:
		if v.Now.Sub(l.since(comp.Typed)) > l.Timing.AckTimeout {
			if comp.Tries < MaxTries {
				l.update(func(st *state.State) { st.Run.Compaction.Status = state.CompactQueued })
				l.Store.Event("compact_retry", map[string]any{"epoch": comp.Epoch, "tries": comp.Tries})
			} else {
				l.fail(comp, fmt.Sprintf("typed /compact %d times but it never started", comp.Tries))
			}
		}
	case state.CompactActive:
		if v.Now.Sub(l.since(comp.Started)) > l.Timing.CompactTimeout {
			l.fail(comp, fmt.Sprintf("the compaction did not finish within %s", l.Timing.CompactTimeout))
		}
	case state.CompactFailed:
		l.retryFailed(v, st)
	case state.CompactDone:
		l.checkResumed(v, st, in)
	}
	l.watch(v, st, in)
	if !l.wished {
		l.heldWhy = ""
	}
}

// Flush sends any queued notices now; the host calls it when the session ends.
func (l *Loop) Flush() {
	if st, err := l.Store.Load(); err == nil {
		l.sendNotices(host.View{Now: l.Store.Now()}, st)
	}
}

// clock notices the machine sleeping. Go's monotonic clock stops during sleep on macOS, so the gap is
// measured on the wall clock. After a sleep, every deadline starts over from the wake: the session
// could not have done anything while the machine was asleep.
func (l *Loop) clock(v host.View) {
	if l.lastTick.IsZero() {
		l.wokeAt = v.Now
	} else if gap := v.Now.Round(0).Sub(l.lastTick.Round(0)); gap > l.Timing.ClockJump {
		l.logf("loop: clock jumped %s (sleep?); timers restart", gap.Round(time.Second))
		l.Store.Event("woke", map[string]any{"slept": gap.Round(time.Second).String()})
		l.wokeAt = v.Now
	}
	l.lastTick = v.Now
}

// since is t, or the last wake if that is later: deadlines do not run while the machine sleeps.
func (l *Loop) since(t time.Time) time.Time { return latest(t, l.wokeAt) }

// Gate reasons.
const (
	gateTurn   = "a turn is in progress"
	gateDialog = "a dialog is open"
	gateAgents = "background subagents are running"
	gateDraft  = "the human has a draft in the input box"
	gateTyping = "the human is typing"
	gateScreen = "the screen is still updating"
)

// gate says why baton must not type right now, or "" if it may. It runs once per tick.
//
// Two gates expire instead of holding forever. A turn that shows no sign of life for StaleTurn is not
// trusted to be open: the Stop hook does not run when a turn is interrupted, and a hook can fail.
// (Claude Code's idle notification usually corrects the flag within a minute; this is the backstop.)
// And a screen that never stays still for Quiet, while everything else allows typing, is ignored after
// QuietMax: something is redrawing it on a timer, such as a status line with a clock.
func (l *Loop) gate(v host.View, st state.State) string {
	why := ""
	switch {
	case st.Run.TurnOpen && !l.turnStale(v, st):
		why = gateTurn
	case st.Run.Dialog != nil:
		why = gateDialog
	case busyAgents(st):
		why = gateAgents
	case v.Draft:
		why = gateDraft
	case v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff:
		why = gateTyping
	}
	if why != "" {
		l.openSince = time.Time{}
		return why
	}
	if l.openSince.IsZero() {
		l.openSince = v.Now
	}
	if v.Now.Sub(v.LastOutput) < l.Timing.Quiet {
		if v.Now.Sub(l.openSince) < l.Timing.QuietMax {
			return gateScreen
		}
		if !l.noisyLog {
			l.noisyLog = true
			l.logf("loop: the screen never stays still for %s (a status line refreshing on a timer?); typing anyway", l.Timing.Quiet)
			l.Store.Event("screen_never_quiet", nil)
		}
	}
	return ""
}

// turnStale reports an open turn with no hook activity for StaleTurn.
func (l *Loop) turnStale(v host.View, st state.State) bool {
	r := st.Run
	if !r.TurnOpen || v.Now.Sub(latest(r.LastActivity, r.TurnStarted, l.wokeAt)) < l.Timing.StaleTurn {
		return false
	}
	if !l.staleLog.Equal(r.TurnStarted) {
		l.staleLog = r.TurnStarted
		l.Store.Event("turn_stale", map[string]any{"started": r.TurnStarted.UTC().Format(time.RFC3339)})
	}
	return true
}

// busyAgents reports background subagents or workflows. Background shells (a dev server, a long build)
// do not hold a compaction: they keep running across it and report when they finish. (Foreground
// subagents need no check of their own: they run inside the main turn, which the turn gate covers.)
func busyAgents(st state.State) bool {
	for _, t := range st.Run.BusyBackground() {
		if t.Type == "subagent" || t.Type == "workflow" {
			return true
		}
	}
	return false
}

// held is called whenever baton needs to type something and the gate says no. Most gates clear by
// themselves; the two that depend on someone else are reported once they have held baton back too long:
// a human draft (the human must send or clear it) and background subagents (which may have hung).
func (l *Loop) held(v host.View, st state.State, task string) {
	l.wished = true
	if l.heldWhy != l.why {
		l.heldWhy, l.heldSince = l.why, v.Now
	}
	var limit time.Duration
	var kind, reason string
	switch l.why {
	case gateDraft:
		limit, kind = l.Timing.DraftEscalate, "draft"
		reason = fmt.Sprintf("baton needs to %s, but there is a draft in the input box: send it with Enter, or clear it with Ctrl-C", task)
	case gateAgents:
		limit, kind = l.Timing.BackgroundMax, "stuck"
		reason = fmt.Sprintf("baton has needed to %s for %s, but background subagents are still running", task, l.Timing.BackgroundMax)
	default:
		return
	}
	if v.Now.Sub(l.since(l.heldSince)) < limit {
		return
	}
	if e := st.Run.Escalation; e == nil || e.Kind != kind {
		l.escalate(kind, reason)
	}
}

// proceed is called when baton is about to type: whatever held it back is over.
func (l *Loop) proceed() {
	l.wished = true
	l.heldWhy = ""
}

func (l *Loop) typeCompact(v host.View, st state.State, in host.Injector) {
	comp := st.Run.Compaction
	if l.why != "" {
		l.held(v, st, "compact the context")
		return
	}
	l.proceed()
	text := "/compact"
	if comp.Tries > 0 || comp.Rounds > 0 {
		text = clearLine + text
	}
	l.update(func(st *state.State) {
		c := &st.Run.Compaction
		c.Status, c.Typed, c.Tries = state.CompactTyped, v.Now, c.Tries+1
	})
	l.Store.Event("compact_typed", map[string]any{"epoch": comp.Epoch, "try": comp.Tries + 1, "round": comp.Rounds + 1, "reason": comp.Reason})
	if err := in.Type(text, true); err != nil {
		l.logf("loop: typing /compact: %v", err)
	}
}

// retryFailed starts a failed compaction over, with backoff. It stays owed (the model is held until it
// happens), so giving up would park the run for good.
func (l *Loop) retryFailed(v host.View, st state.State) {
	comp := st.Run.Compaction
	if comp.Rounds+1 >= MaxRounds || !(st.BoundaryOwed || st.CheckpointOwed) {
		return
	}
	wait := l.Timing.FailedRetry
	for i := 0; i < comp.Rounds; i++ {
		wait *= 3
	}
	if v.Now.Sub(l.since(comp.FailedAt)) < wait {
		return
	}
	l.update(func(st *state.State) {
		c := &st.Run.Compaction
		c.Status, c.Tries, c.Rounds = state.CompactQueued, 0, c.Rounds+1
	})
	l.Store.Event("compact_requeued", map[string]any{"epoch": comp.Epoch, "round": comp.Rounds + 2})
}

// checkResumed makes sure the model actually started working after a compaction baton asked for.
func (l *Loop) checkResumed(v host.View, st state.State, in host.Injector) {
	comp := st.Run.Compaction
	if !comp.ByBaton || comp.Finished.IsZero() {
		return
	}
	if (st.Run.TurnOpen && l.why == gateTurn) || st.Run.TurnStarted.After(comp.Finished) {
		return // it resumed
	}
	switch {
	case comp.Nudged.IsZero() && v.Now.Sub(l.since(comp.Finished)) > l.Timing.ResumeNudge:
		if l.why != "" {
			l.held(v, st, "remind the model to resume after the compaction")
			return
		}
		l.proceed()
		verb := "Begin"
		if comp.Reason == "checkpoint" {
			verb = "Continue"
		}
		msg := fmt.Sprintf("[baton] Context compacted. %s %s now — the brief above has your instructions.", verb, st.Current)
		l.update(func(st *state.State) { st.Run.Compaction.Nudged = v.Now })
		l.Store.Event("resume_nudge", map[string]any{"epoch": comp.Epoch})
		in.Type(msg, true)
	case !comp.Nudged.IsZero() && v.Now.Sub(l.since(comp.Nudged)) > l.Timing.ResumeEscalate:
		if e := st.Run.Escalation; e == nil || e.Kind != "stalled" {
			l.escalate("stalled", fmt.Sprintf("the model did not resume %s after the compaction", st.Current))
		}
	}
}

func (l *Loop) fail(comp state.Compaction, reason string) {
	l.update(func(st *state.State) {
		st.Run.Compaction.Status, st.Run.Compaction.FailedAt = state.CompactFailed, l.Store.Now()
	})
	l.Store.Event("compact_failed", map[string]any{"epoch": comp.Epoch, "round": comp.Rounds + 1, "reason": reason})
	if comp.Rounds+1 < MaxRounds {
		reason += "; baton will try again"
	} else {
		reason += "; type /compact yourself once the turn is over"
	}
	l.escalate("compaction_failed", reason)
}

// escalate records a watchdog escalation and queues a notice; the next tick sends it.
func (l *Loop) escalate(kind, reason string) {
	now := l.Store.Now()
	l.update(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Kind: kind, Reason: reason, Since: now, Watchdog: true, LastPush: now}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: kind, Text: reason, At: now})
	})
	l.Store.Event("escalated", map[string]any{"type": kind, "reason": reason})
}

// resolve clears a watchdog escalation once its cause is gone, so it neither misleads the human nor
// keeps the watchdog quiet for the rest of the run. Escalations from the Stop table (blocked, repeated
// stops) are not the watchdog's to clear: they wait for the human's answer or for the plan to move.
func (l *Loop) resolve(v host.View, st state.State) {
	e := st.Run.Escalation
	if e == nil || !e.Watchdog {
		return
	}
	var gone bool
	switch e.Kind {
	case "stalled":
		gone = st.Run.LastActivity.After(e.Since) || st.Run.TurnStarted.After(e.Since)
	case "draft":
		gone = !v.Draft
	case "stuck":
		gone = l.why != gateAgents
	case "compaction_failed":
		gone = st.Run.Compaction.Status == state.CompactDone || !(st.BoundaryOwed || st.CheckpointOwed)
	case "api_error":
		gone = st.Run.LastError == nil || st.Run.TurnStarted.After(st.Run.LastError.At) && st.Run.LastStop.After(st.Run.LastError.At)
	}
	if !gone {
		return
	}
	l.update(func(st *state.State) {
		if st.Run.Escalation != nil && st.Run.Escalation.Since.Equal(e.Since) {
			st.Run.Escalation = nil
		}
	})
	l.Store.Event("escalation_cleared", map[string]any{"type": e.Kind})
}

// remind pushes an unresolved escalation again, a few times, further and further apart: one push is
// easy to miss, and a run waiting on the human loses hours until they see it.
func (l *Loop) remind(v host.View, st state.State) {
	e := st.Run.Escalation
	if e == nil || e.Reminded >= len(Reminders) || v.Now.Sub(e.Since) < Reminders[e.Reminded] {
		return
	}
	text := fmt.Sprintf("still waiting on you, since %s: %s", e.Since.Local().Format("15:04"), e.Reason)
	l.update(func(st *state.State) {
		if x := st.Run.Escalation; x != nil && x.Since.Equal(e.Since) {
			x.Reminded++
			x.LastPush = v.Now
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: e.Kind, Text: text, At: v.Now})
		}
	})
	l.Store.Event("reminded", map[string]any{"type": e.Kind, "count": e.Reminded + 1})
}

// sendNotices pushes every queued notice that is due. One that fails stays queued and is retried a
// couple of times: a push lost to a network blip may be the only one the human would have seen.
func (l *Loop) sendNotices(v host.View, st state.State) {
	type result struct {
		at  time.Time
		err error
	}
	var results []result
	for _, n := range st.Run.Notices {
		if v.Now.Before(n.NextTry) {
			continue
		}
		err := l.Notify.Notify(l.Project, n.Kind, n.Text)
		fields := map[string]any{"notice": n.Kind}
		if err != nil {
			fields["error"] = err.Error()
		}
		l.Store.Event("notified", fields)
		results = append(results, result{n.At, err})
	}
	if len(results) == 0 {
		return
	}
	l.update(func(st *state.State) {
		var keep []state.Notice
		for _, n := range st.Run.Notices {
			for _, r := range results {
				if !r.at.Equal(n.At) {
					continue
				}
				if r.err == nil || n.Tries+1 >= 3 {
					n.Kind = "" // sent, or given up on
				} else {
					n.Tries, n.NextTry = n.Tries+1, v.Now.Add(l.Timing.NoticeRetry<<n.Tries)
				}
			}
			if n.Kind != "" {
				keep = append(keep, n)
			}
		}
		st.Run.Notices = keep
		if st.Run.Escalation != nil {
			st.Run.Escalation.Pushed = true
		}
	})
}

func (l *Loop) update(fn func(*state.State)) {
	if _, err := l.Store.Update(func(st *state.State) error { fn(st); return nil }); err != nil {
		l.logf("loop: update: %v", err)
	}
}

func (l *Loop) logf(format string, a ...any) {
	if l.Logf != nil {
		l.Logf(format, a...)
	}
}
