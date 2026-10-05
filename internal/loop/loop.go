// Package loop is the host's controller: on every tick it reads the project state the hooks keep, and
// it is the only part of baton that acts on its own. It types /compact when a compaction is queued and
// it is safe to type, demands proof the compaction happened, nudges a model that did not resume, and
// sends the human the notices the hooks queued.
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
	HandsOff       time.Duration // and the human must not have typed for this long
	AckTimeout     time.Duration // PreCompact must fire this soon after typing /compact
	CompactTimeout time.Duration // PostCompact must fire this soon after PreCompact
	ResumeNudge    time.Duration // no turn this long after a compaction: type a reminder
	ResumeEscalate time.Duration // still nothing this long after the reminder: bring the human in
	DraftEscalate  time.Duration // a human draft blocking a compaction this long: tell them
}

// DefaultTiming is baton's production timing.
var DefaultTiming = Timing{
	Quiet: 1500 * time.Millisecond, HandsOff: 3 * time.Second, AckTimeout: 15 * time.Second,
	CompactTimeout: 10 * time.Minute, ResumeNudge: 60 * time.Second, ResumeEscalate: 3 * time.Minute,
	DraftEscalate: 2 * time.Minute,
}

// MaxTries is how many times baton types /compact for one compaction before escalating.
const MaxTries = 2

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

	draftSince time.Time
}

// Tick runs once per host tick.
func (l *Loop) Tick(v host.View, in host.Injector) {
	if !v.Owner {
		return
	}
	st, err := l.Store.Load()
	if err != nil {
		l.logf("loop: %v", err)
		return
	}
	l.sendNotices(st)
	if st.Mode != state.ModeRunning {
		return
	}
	comp := st.Run.Compaction
	switch comp.Status {
	case state.CompactQueued:
		l.typeCompact(v, st, in)
	case state.CompactTyped:
		if v.Now.Sub(comp.Typed) > l.Timing.AckTimeout {
			if comp.Tries < MaxTries {
				l.update(func(st *state.State) { st.Run.Compaction.Status = state.CompactQueued })
				l.Store.Event("compact_retry", map[string]any{"epoch": comp.Epoch, "tries": comp.Tries})
			} else {
				l.fail(comp, "compaction_failed", fmt.Sprintf("typed /compact %d times but it never started", comp.Tries))
			}
		}
	case state.CompactActive:
		if v.Now.Sub(comp.Started) > l.Timing.CompactTimeout {
			l.fail(comp, "compaction_failed", fmt.Sprintf("the compaction did not finish within %s", l.Timing.CompactTimeout))
		}
	case state.CompactDone:
		l.checkResumed(v, st, in)
	}
}

// gate says why baton must not type right now, or "" if it may.
func (l *Loop) gate(v host.View, st state.State) string {
	switch {
	case st.Run.TurnOpen:
		return "a turn is in progress"
	case st.Run.Dialog != nil:
		return "a dialog is open (" + st.Run.Dialog.Tool + ")"
	case st.Run.Subagents > 0 || busyAgents(st):
		return "subagents are running"
	case v.Draft:
		return "the human has a draft in the input box"
	case v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff:
		return "the human is typing"
	case v.Now.Sub(v.LastOutput) < l.Timing.Quiet:
		return "the screen is still updating"
	}
	return ""
}

// busyAgents reports background subagents or workflows. Background shells (a dev server, a long build)
// do not hold a compaction: they keep running across it and report when they finish.
func busyAgents(st state.State) bool {
	for _, t := range st.Run.BusyBackground() {
		if t.Type == "subagent" || t.Type == "workflow" {
			return true
		}
	}
	return false
}

func (l *Loop) typeCompact(v host.View, st state.State, in host.Injector) {
	comp := st.Run.Compaction
	why := l.gate(v, st)
	if why != "" {
		if v.Draft {
			if l.draftSince.IsZero() {
				l.draftSince = v.Now
			} else if v.Now.Sub(l.draftSince) > l.Timing.DraftEscalate && (st.Run.Escalation == nil || st.Run.Escalation.Reason != draftReason) {
				l.escalate("draft", draftReason)
			}
		} else {
			l.draftSince = time.Time{}
		}
		return
	}
	l.draftSince = time.Time{}
	text := "/compact"
	if comp.Tries > 0 {
		text = clearLine + text
	}
	l.update(func(st *state.State) {
		c := &st.Run.Compaction
		c.Status, c.Typed, c.Tries = state.CompactTyped, v.Now, c.Tries+1
	})
	l.Store.Event("compact_typed", map[string]any{"epoch": comp.Epoch, "try": comp.Tries + 1, "reason": comp.Reason})
	if err := in.Type(text, true); err != nil {
		l.logf("loop: typing /compact: %v", err)
	}
}

const draftReason = "a compaction is waiting for the draft in the input box (send it or clear it)"

// checkResumed makes sure the model actually started working after a compaction baton asked for.
func (l *Loop) checkResumed(v host.View, st state.State, in host.Injector) {
	comp := st.Run.Compaction
	if !comp.ByBaton || comp.Finished.IsZero() {
		return
	}
	if st.Run.TurnOpen || st.Run.TurnStarted.After(comp.Finished) {
		return // it resumed
	}
	since := v.Now.Sub(comp.Finished)
	switch {
	case comp.Nudged.IsZero() && since > l.Timing.ResumeNudge:
		if l.gate(v, st) != "" {
			return
		}
		verb := "Begin"
		if comp.Reason == "checkpoint" {
			verb = "Continue"
		}
		msg := fmt.Sprintf("[baton] Context compacted. %s %s now — the brief above has your instructions.", verb, st.Current)
		l.update(func(st *state.State) { st.Run.Compaction.Nudged = v.Now })
		l.Store.Event("resume_nudge", map[string]any{"epoch": comp.Epoch})
		in.Type(msg, true)
	case !comp.Nudged.IsZero() && v.Now.Sub(comp.Nudged) > l.Timing.ResumeEscalate:
		reason := fmt.Sprintf("the model did not resume %s after the compaction", st.Current)
		if st.Run.Escalation == nil || st.Run.Escalation.Reason != reason {
			l.escalate("stalled", reason)
		}
	}
}

func (l *Loop) fail(comp state.Compaction, kind, reason string) {
	l.update(func(st *state.State) { st.Run.Compaction.Status = state.CompactFailed })
	l.Store.Event("compact_failed", map[string]any{"epoch": comp.Epoch, "reason": reason})
	l.escalate(kind, reason)
}

// escalate records an escalation and queues a notice; the next tick sends it.
func (l *Loop) escalate(kind, reason string) {
	l.update(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Reason: reason, Since: l.Store.Now()}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: kind, Text: reason, At: l.Store.Now()})
	})
	l.Store.Event("escalated", map[string]any{"type": kind, "reason": reason})
}

// sendNotices pushes every queued notice once, then removes it.
func (l *Loop) sendNotices(st state.State) {
	if len(st.Run.Notices) == 0 {
		return
	}
	sent := map[time.Time]bool{}
	for _, n := range st.Run.Notices {
		err := l.Notify.Notify(l.Project, n.Kind, n.Text)
		fields := map[string]any{"notice": n.Kind}
		if err != nil {
			fields["error"] = err.Error()
		}
		l.Store.Event("notified", fields)
		sent[n.At] = true
	}
	l.update(func(st *state.State) {
		var keep []state.Notice
		for _, n := range st.Run.Notices {
			if !sent[n.At] {
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
