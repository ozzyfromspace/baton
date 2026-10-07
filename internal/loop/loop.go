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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
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
	DraftGrace     time.Duration // a draft holding baton this long is saved and cleared, and baton carries on
	RescueBackoff  time.Duration // and the box is not cleared again within this, however the flag reads
	DraftEscalate  time.Duration // a human draft blocking baton this long: tell them
	StaleTurn      time.Duration // an open turn with no hook activity this long is not trusted to be open
	IdleNudge      time.Duration // no hook activity this long mid-plan: type a reminder (then escalate)
	WaitGrace      time.Duration // extra time after a declared wait runs out
	BackgroundMax  time.Duration // background work may hold a quiet session (or a compaction) this long
	DialogNotify   time.Duration // a permission prompt or question open this long: notify the human
	WarnTimeout    time.Duration // the context question unanswered this long: baton answers "Keep going"
	RateLimitRetry time.Duration // retry interval after a usage-limit error (doubled once at most)
	OverloadRetry  time.Duration // first retry after an overload or server error (then backoff)
	ClockJump      time.Duration // a gap between ticks this long means the machine slept
	NoticeRetry    time.Duration // a notice that failed to send is retried after this

	// EscalationTimeout is how long a proposal waits for the human before baton goes ahead with it
	// (escalation_timeout); a key the human presses starts it over.
	EscalationTimeout time.Duration
}

// DefaultTiming is baton's production timing.
var DefaultTiming = Timing{
	Quiet: 1500 * time.Millisecond, QuietMax: 30 * time.Second, HandsOff: 3 * time.Second,
	AckTimeout: 15 * time.Second, CompactTimeout: 10 * time.Minute, FailedRetry: 5 * time.Minute,
	ResumeNudge: 60 * time.Second, ResumeEscalate: 3 * time.Minute,
	DraftGrace: 20 * time.Second, RescueBackoff: 2 * time.Minute, DraftEscalate: 2 * time.Minute,
	StaleTurn: 15 * time.Minute, IdleNudge: 10 * time.Minute, WaitGrace: time.Minute, BackgroundMax: 30 * time.Minute,
	DialogNotify: 3 * time.Minute, WarnTimeout: valve.DefaultWarnTimeout, RateLimitRetry: 15 * time.Minute, OverloadRetry: time.Minute,
	ClockJump: 2 * time.Minute, NoticeRetry: time.Minute, EscalationTimeout: config.DefaultEscalationTimeout,
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
	// Snapshot saves uncommitted work (gitx.Snapshot when nil).
	Snapshot func(root string, at time.Time, phase, why string) (gitx.Saved, error)

	why       string    // this tick's gate: why baton must not type now ("" if it may)
	openSince time.Time // since when every gate but the quiet screen has been open
	noisyLog  bool      // the never-quiet screen was reported
	staleLog  time.Time // the turn whose staleness was reported
	dialogLog time.Time // the dialog whose staleness was reported
	agentsLog bool      // background subagents have been overridden once
	rescues   int       // drafts taken out of the input box this session
	rescuedAt time.Time // when the last one was taken
	heldWhy   string    // what has been holding back something baton needs to type
	heldSince time.Time
	wished    bool // something wanted to type this tick

	alone   state.Dialog // baton's own question, alone on screen since aloneAt
	aloneAt time.Time

	owner    bool           // this session has owned the project
	quit     bool           // claude was asked to end: the human is leaving baton
	halted   bool           // the run is halted, and this halt's snapshot was started
	snapping atomic.Bool    // a snapshot is running
	saving   sync.WaitGroup // and Ended waits for it

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
	if v.Store != nil && (l.Store == nil || v.Store.Dir != l.Store.Dir) {
		l.drive(v.Store)
	}
	if !v.Owner || l.Store == nil {
		return
	}
	l.owner = true
	l.clock(v)
	st, err := l.Store.Load()
	if err != nil {
		l.logf("loop: %v", err)
		return
	}
	l.sendNotices(v, st)
	l.saveWork(v, st)
	if !st.Run.ExitAt.IsZero() {
		l.leave(v, st, in)
		return
	}
	if st.Mode != state.ModeRunning {
		return
	}
	l.why = l.gate(v, st)
	l.wished = false
	l.resolve(v, st)
	l.remind(v, st)

	// A draft must never be able to hold a run. If one is in the way of something baton needs to type,
	// save it where `baton drafts` hands it back, clear the box, and carry on.
	if l.why == gateDraft && l.owed(st) && l.rescueDraft(v, st, in) {
		// The box is empty as of a moment ago; the host's next view will say so too. Re-gate without
		// it so the work baton was holding goes out on this tick rather than the next.
		v.Draft, v.DraftText = false, ""
		l.why = l.gate(v, st)
	}

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
	l.publishGate(st)
}

// publishGate records the current gate so the status line can say what a run is waiting for. Written
// only when it changes: a tick is 250ms and this is a file.
func (l *Loop) publishGate(st state.State) {
	shown := l.why
	if !l.wished {
		shown = "" // nothing wanted to type, so nothing is being held up
	}
	if shown == st.Run.Gate {
		return
	}
	l.update(func(st *state.State) { st.Run.Gate = shown })
}

// leave ends claude for a human leaving baton, once the turn is over and nothing is on screen, so the
// conversation is resumed (as plain Claude Code) between turns.
func (l *Loop) leave(v host.View, st state.State, in host.Injector) {
	if l.quit || st.Run.TurnOpen && !l.turnStale(v, st) || st.Run.Dialogs.AnyOpen() || v.Now.Sub(v.LastOutput) < l.Timing.Quiet {
		return
	}
	l.quit = true
	l.Store.Event("left_baton", nil)
	if err := in.Quit(); err != nil {
		l.logf("loop: quitting: %v", err)
	}
}

// drive switches the loop to the run s: the one the session was bound to once it started, or another one
// after the human switched conversations. What the loop remembered about the previous run's progress
// does not carry over; what it knows about the terminal does.
func (l *Loop) drive(s *state.Store) {
	if l.Store != nil {
		l.logf("loop: now driving %s (was %s)", s.Dir, l.Store.Dir)
	}
	l.Store = s
	l.halted, l.staleLog, l.dialogLog, l.idleNudged, l.dialogNotified = false, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	l.rateNotified, l.errNudged, l.errRetries = false, time.Time{}, 0
	l.alone, l.aloneAt, l.heldWhy, l.heldSince = state.Dialog{}, time.Time{}, "", time.Time{}
}

// Flush sends any queued notices now; the host calls it when the session ends.
func (l *Loop) Flush() {
	if l.Store == nil {
		return
	}
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
	case st.Run.Dialogs.AnyOpen():
		why = gateDialog
	case busyAgents(st) && !l.agentsStale(v, st):
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

// gateDialog has NO ceiling, deliberately, and it is the one gate that keeps one.
//
// `Run.Dialogs` holds what Claude Code waits on the human for — "a permission prompt or a question".
// Keystrokes landing in a permission prompt SELECT an option, so a baton that stopped trusting that
// flag and typed anyway could approve a tool call nobody approved. Every other gate expires into
// action; this one may not, because the thing on the other side of it is the human's consent. A plan
// that waits for an answer is working as intended, and that wait is escalated and pushed.
//
// agentsStale reports background subagents that have held baton for BackgroundMax. They used to hold it
// for good: the gate escalated once and then waited on a human. A hung subagent must not be able to
// park a plan, and a compaction is safe to take while one runs — the work reports when it finishes.
func (l *Loop) agentsStale(v host.View, st state.State) bool {
	if l.heldWhy != gateAgents || v.Now.Sub(l.since(l.heldSince)) < l.Timing.BackgroundMax {
		return false
	}
	if !l.agentsLog {
		l.agentsLog = true
		l.logf("loop: background subagents have held baton for %s; carrying on anyway", l.Timing.BackgroundMax)
		l.Store.Event("agents_overridden", map[string]any{"after": l.Timing.BackgroundMax.String()})
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

// owed reports work baton must type to make progress: a compaction at a boundary or a checkpoint. A
// nudge is deliberately not owed — a reminder arriving late costs nothing, and clearing somebody's input
// box to send one would be rude.
func (l *Loop) owed(st state.State) bool {
	switch st.Run.Compaction.Status {
	case state.CompactQueued, state.CompactFailed:
		return true
	}
	return st.BoundaryOwed || st.CheckpointOwed
}

// rescueDraft saves the draft in the input box and clears it, so the gate opens on the next tick. A
// half-typed message used to hold a run for as long as it sat there: measured 2026-10-05, a plan sat
// with a compaction queued for 35 minutes, and only a person noticing could have cleared it.
//
// Two conditions, and both matter. HandsOff means nobody has touched a key for a moment, so this never
// snatches a line out from under someone mid-word. DraftGrace means the draft has actually been in
// baton's way for a while, so an ordinary message being composed and sent is never touched — sending it
// clears the box anyway.
//
// An empty draft is the dead-reckoning case: keyTracker counts keystrokes and errs toward "there is a
// draft", so it can drift there with nothing in the box. SaveDraft writes nothing for that, and the
// clear puts the flag back where reality is — so this fixes a phantom draft too, at no cost.
func (l *Loop) rescueDraft(v host.View, st state.State, in host.Injector) bool {
	// Something wants to type, which is what keeps heldSince from being reset at the end of the tick.
	l.wished = true
	if v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff {
		return false
	}
	if l.heldWhy != gateDraft {
		l.heldWhy, l.heldSince = gateDraft, v.Now
		return false
	}
	if v.Now.Sub(l.since(l.heldSince)) < l.Timing.DraftGrace {
		return false
	}
	// A flag that stays up however often it is cleared is the stuck-flag case this whole change exists
	// for, and clearing the box on every tick would make baton the thing destroying somebody's typing.
	// One clear per backoff: the run still moves, and the cost of a wrong reading stays bounded.
	if !l.rescuedAt.IsZero() && v.Now.Sub(l.since(l.rescuedAt)) < l.Timing.RescueBackoff {
		return false
	}
	path, err := l.Store.SaveDraft(v.DraftText, st.Current, v.Now)
	if err != nil {
		l.logf("loop: saving the draft: %v", err)
		return false // never throw away text baton failed to save
	}
	if err := in.ClearInput(); err != nil {
		l.logf("loop: clearing the input box: %v", err)
		return false
	}
	l.rescues, l.rescuedAt = l.rescues+1, v.Now
	l.heldWhy = ""
	fields := map[string]any{"chars": len(v.DraftText)}
	if path != "" {
		fields["path"] = path
		l.logf("loop: saved a %d-character draft to %s and cleared the input box", len(v.DraftText), path)
	} else {
		l.logf("loop: the input box tracked a draft with no text in it; cleared")
	}
	l.Store.Event("draft_rescued", fields)
	return true
}

// held is called whenever baton needs to type something and the gate says no. Most gates clear by
// themselves or expire into action; held only REPORTS, so nothing it is told about can hold a run. The
// exception is gateDialog, which is the human's consent and keeps its wait (see the note above).
func (l *Loop) held(v host.View, st state.State, task string) {
	l.wished = true
	if l.heldWhy != l.why {
		l.heldWhy, l.heldSince = l.why, v.Now
	}
	var limit time.Duration
	var kind, reason string
	switch l.why {
	case gateDraft:
		// Kept as a notice only. rescueDraft clears the draft well before this, so seeing it means the
		// rescue itself is failing — which is worth telling someone about, not worth waiting on.
		limit, kind = l.Timing.DraftEscalate, "draft"
		reason = fmt.Sprintf("baton needs to %s and could not clear the draft in the input box; it will keep trying", task)
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
