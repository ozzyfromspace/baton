package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
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
	l.answerOwnQuestion(v, st, in)
	l.lapse(v, st)
	comp := st.Run.Compaction
	if comp.InFlight() || comp.Status == state.CompactFailed || l.resuming(st) {
		return // the compaction checks own this stretch
	}
	// An API error that ended the last turn is retried even while an older escalation is open: the
	// escalation is about something else, and the error would otherwise leave the run parked.
	if e := st.Run.LastError; e != nil && !st.Run.TurnStarted.After(e.At) {
		if esc := st.Run.Escalation; esc == nil || errorKinds[esc.Kind] || e.At.After(esc.Since) {
			l.watchError(v, st, in, *e)
			return
		}
	}
	l.errRetries, l.rateNotified = 0, false

	if st.Run.Escalation != nil || st.Blocked != nil || l.why == gateTurn || st.Run.Dialogs.AnyOpen() {
		l.idleNudged = time.Time{} // the human has it, or the model is working: nothing is stalled
		return
	}
	if l.why == gateUsage {
		l.idleNudged = time.Time{} // the account waits on its usage limit, and so does every session of the run
		return
	}
	if l.deliver(v, st, in) {
		l.idleNudged = time.Time{}
		return
	}
	if st.Current == "" {
		// No phase is in hand: every phase left waits for a job, which ends by itself (verify_timeout,
		// or its runner found dead), and baton takes the next phase up then. Nothing is stalled.
		l.idleNudged = time.Time{}
		return
	}
	if st.CurrentJob() != nil {
		// The phase in hand waits on its job, which ends by itself: its result wakes the session, unless
		// baton switches it to another phase first. Nothing is stalled.
		l.idleNudged = time.Time{}
		return
	}
	if !l.idleNudged.IsZero() && st.Run.LastActivity.After(l.idleNudged) {
		l.idleNudged = time.Time{} // the model answered the reminder (IdleNudges still counts it)
	}
	quietSince := latest(st.Run.LastActivity, st.Run.LastStop, l.wokeAt, l.idleNudged)
	if busy := st.Run.BusyBackground(); len(busy) > 0 && v.Now.Sub(l.since(backgroundSince(busy, st.Run.LastStop))) < l.Timing.BackgroundIdle {
		return // the model's own background work wakes the session when it ends; baton looks in once it has run long
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
		// The model answers reminders but the plan does not move (background work that runs on and on):
		// more reminders would only burn tokens.
		l.escalate("stalled", fmt.Sprintf("%s has not moved after %d reminders", st.Current, st.Run.IdleNudges))
		return
	}
	if l.why != "" {
		l.held(v, st, "remind the idle model")
		return
	}
	l.proceed()
	if err := l.say(v, in, l.idleMessage(v, st)); err != nil {
		l.unsaid(err, "reminding the idle model")
		return
	}
	l.idleNudged = v.Now
	l.update(func(st *state.State) { st.Run.IdleNudges++ })
	l.Store.Event("idle_nudge", map[string]any{"phase": st.Current, "quiet_for": v.Now.Sub(quietSince).Round(time.Second).String(), "count": st.Run.IdleNudges + 1, "via": l.via()})
}

// resuming reports the stretch after a compaction baton asked for, until the model's turn after it begins:
// the compaction checks own it (checkResumed). A phase agent has no such stretch: its compactions are
// proven by its own transcript and briefed again by its loop (Agent.rebrief), and the checkpoint a fresh
// agent ends (state.Fresh) is over as that agent's turn starts, at the very instant its start records.
func (l *Loop) resuming(st state.State) bool {
	comp := st.Run.Compaction
	return l.Store.Worker() == "" && comp.Status == state.CompactDone && comp.ByBaton && !st.Run.TurnStarted.After(comp.Finished)
}

func (l *Loop) idleMessage(v host.View, st state.State) string {
	switch {
	case len(st.Run.BusyBackground()) > 0:
		return fmt.Sprintf("[baton] Background work has been running for over %s with no progress on %s. Check on it, then continue, or report your status.", decide.Spell(l.Timing.BackgroundIdle), st.Current)
	default:
		return fmt.Sprintf("[baton] No activity for %s and %s is not reported done. Continue it, or report: baton done / baton propose / baton blocked --tried.", l.Timing.IdleNudge, st.Current)
	}
}

// backgroundSince is when the oldest of the background work started running, as far as baton knows: when a
// Stop first reported it (at the latest, the last Stop).
func backgroundSince(busy []state.Task, lastStop time.Time) time.Time {
	if t := state.Oldest(busy); !t.IsZero() && t.Before(lastStop) {
		return t
	}
	return lastStop
}

// watchDialog tells the human when Claude Code has been waiting on them (a permission prompt or a
// question) for a while. It is a notice, not an escalation: the session is fine, it needs an answer.
// It is repeated on the same schedule as escalation reminders, since the run waits until it is answered.
// A phase agent's prompt shows in the lead's terminal, and its notice names the phase that waits.
func (l *Loop) watchDialog(v host.View, st state.State) {
	d, waiting := st.Run.Dialogs.Front()
	if !waiting {
		return
	}
	if _, alone := st.Run.Dialogs.Only(); alone && d.Kind == "proposal" {
		if p := st.PendingProposal(); p != nil && p.Untimed == "" {
			return // the human was told when it was asked, and baton answers it at the deadline
		}
	}
	if _, alone := st.Run.Dialogs.Only(); alone && d.Kind == "relay" && relayOnScreen(st, d.Key) != nil {
		return // a phase agent's question: baton answers it if nobody does, and the lead goes on
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
	text := "waiting for your answer (" + d.Tool + ")"
	if phase := agentPhase(st, d.Agent); phase != "" {
		text = decide.AgentWaits(phase, d.Tool)
	}
	l.update(func(st *state.State) {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "dialog", Text: text, At: v.Now})
	})
	fields := map[string]any{"tool": d.Tool, "count": n}
	if phase := agentPhase(st, d.Agent); phase != "" {
		fields["phase"] = phase
	}
	l.Store.Event("dialog_waiting", fields)
}

// agentPhase is the phase of the run's phase agent agent ("" for none, and for the lead's own).
func agentPhase(st state.State, agent string) string {
	if agent == "" {
		return ""
	}
	for _, w := range st.LiveWorkers() {
		if w.AgentID == agent {
			return w.Phase
		}
	}
	return ""
}

// answerOwnQuestion answers baton's own questions that have a default, when nobody has: the context
// question (Keep going) after WarnTimeout, since it only warns and Claude Code compacts on its own when
// the context is full; and a timed proposal (Go ahead) at its deadline, since a run must not stop for
// something it can work around (docs/design.md). An untimed proposal is never answered: only the
// human can let it go ahead. The answer arrives through the usual hook.
//
// It types only where baton's mod does not run (serial mode): where it does, the mod settles each of
// baton's questions at its deadline with no key typed (hooks.Bound), and nothing types into a dialog; nor
// while a mod that may yet say hello is waited for (modWaiting).
//
// A key typed into the wrong dialog can approve something nobody approved (in a permission prompt, 2 is
// "Yes, and always allow"), so three layers keep it out of anyone else's (docs/research/escalation.md):
//   - baton's question must be the only dialog open, so nothing can be on screen in front of it;
//   - the hooks mark an entry as baton's only for the exact question baton issued;
//   - the key is 3, where the default sits, which in a permission prompt means "No".
//
// The clock runs from when the question was first alone, and every key the human presses starts it over:
// somebody at the keyboard is answering it. A proposal is never answered before the time its question
// names, nor less than MinProposalWait after it was first alone (after a sleep, the human gets that long
// again). Keys go to the dialog, not the input box, so a draft there does not matter. It answers once
// per question.
func (l *Loop) answerOwnQuestion(v host.View, st state.State, in host.Injector) {
	d, alone := st.Run.Dialogs.Only()
	if !alone || !d.AutoAnswered.IsZero() || l.mode != modOff {
		return
	}
	var due time.Time
	fields := map[string]any{"dialog": d.Kind, "key": ownAnswerKey}
	switch d.Kind {
	case "context_warning":
		if !l.alone.Same(d) {
			l.alone, l.aloneAt = d, v.Now
		}
		due = l.since(latest(d.Since, l.aloneAt, v.LastHumanKey)).Add(l.Timing.WarnTimeout)
	case "proposal":
		p := st.PendingProposal()
		if p == nil || p.Untimed != "" || p.Deadline.IsZero() || decide.Normalize(p.Question) != d.Key {
			return // untimed, settled, or not the question this proposal issued
		}
		if !l.alone.Same(d) {
			l.alone, l.aloneAt = d, v.Now
		}
		// A key is a moment on the wall clock: one pressed before a sleep does not restart the timer at
		// the wake (the minute alone does that).
		due = latest(p.Deadline, l.since(latest(d.Since, l.aloneAt)).Add(MinProposalWait), v.LastHumanKey.Add(l.Timing.EscalationTimeout))
		fields["id"] = p.ID
	default:
		return
	}
	if v.Now.Before(due) || v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff {
		return
	}
	l.update(func(st *state.State) {
		for i := range st.Run.Dialogs {
			if st.Run.Dialogs[i].Same(d) {
				st.Run.Dialogs[i].AutoAnswered = v.Now
			}
		}
	})
	fields["after"] = v.Now.Sub(l.since(l.aloneAt)).Round(time.Second).String()
	l.Store.Event("auto_answered", fields)
	in.Type(ownAnswerKey, false)
}

// ownAnswerKey picks the third option of baton's own questions, where the default sits.
const ownAnswerKey = "3"

// MinProposalWait is the least time a proposal's question is on screen, alone, before baton answers it.
const MinProposalWait = decide.MinWait

// transientErrors are API errors worth retrying: they pass on their own. Anything else (an expired
// login, billing, a model that does not exist, a request Claude Code refuses to send, a turn the model's
// safeguards declined) needs the human.
var transientErrors = map[string]bool{"rate_limit": true, "overloaded": true, "server_error": true, "unknown": true, "max_output_tokens": true}

// errorKinds are the escalations of an API error that ended the last turn: refused for a turn the model's
// safeguards declined, api_error for any other.
var errorKinds = map[string]bool{"api_error": true, "refused": true}

// MaxErrorRetries is how many transient-error retries happen before the human is told (retries go on).
const MaxErrorRetries = 6

// watchError resumes a turn that an API error ended. A usage limit is announced once per episode and
// retried every 15-30 minutes, so the run resumes soon after the limit resets. Overloads and server
// errors are retried with backoff, and reported if they persist. Errors that will not pass on their
// own are reported at once and not retried. A turn that starts on its own (Claude Code may resume by
// itself) supersedes all of this.
//
// A refusal (a turn the model's safeguards declined, which Claude Code would not go on with) is never
// retried, and nothing is typed into its conversation: the same model given the same conversation
// usually declines again, and how the phase goes on is the human's to decide (decide.Refused).
func (l *Loop) watchError(v host.View, st state.State, in host.Injector, e state.StopError) {
	if !transientErrors[e.Error] {
		kind := "api_error"
		if e.Error == state.ErrRefusal {
			kind = "refused"
		}
		if esc := st.Run.Escalation; esc == nil || esc.Kind != kind {
			if kind == "refused" {
				l.escalateWith(kind, decide.Refused(l.stopped(st), e.Category), map[string]any{"category": e.Category})
			} else {
				why := fmt.Sprintf("the last turn failed with an API error baton cannot retry away (%s)", e.Error)
				if e.Details != "" {
					why += ": " + e.Details
				}
				l.escalate(kind, decide.CannotGoOn(l.stopped(st), why))
			}
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
	if err := l.say(v, in, fmt.Sprintf("[baton] The last turn ended with an API error (%s). Continue %s.", e.Error, st.Current)); err != nil {
		l.unsaid(err, "retrying after an API error")
		return
	}
	l.errRetries++
	l.errNudged = v.Now
	l.Store.Event("error_retry", map[string]any{"error": e.Error, "retry": l.errRetries, "via": l.via()})
}

// stopped describes this loop, stopped, for the escalation that says so: its phase, whether it is a
// worker's, whether the run has workers, and the phases left that need its phase, directly or not, which
// cannot start while it is stopped. A worker's plan is narrowed to its phase, so those come from its run's.
func (l *Loop) stopped(st state.State) decide.Stop {
	s := decide.Stop{Phase: st.Current, Worker: l.Store.Worker() != "", Workers: l.Workers}
	if st.Current == "" {
		return s
	}
	store, run := l.Store, st
	if s.Worker {
		parent, err := l.Store.ParentRun()
		if err != nil {
			return s
		}
		if run, err = parent.Load(); err != nil {
			return s
		}
		store = parent
	}
	pl, err := store.LoadPlan()
	if err != nil {
		return s
	}
	for _, id := range pl.Descendants(st.Current) {
		if ps := run.Phases[id]; ps == nil || ps.Status != state.PhaseDone {
			s.Dependents = append(s.Dependents, id)
		}
	}
	return s
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

// lapse goes ahead with the lead's own timed proposal whose question went away unanswered (dismissed)
// once its deadline passes, as baton would have answered it on screen: nobody chose anything, so nothing
// waits on the human for it. The human writing first answers it instead (hooks.vanished). The model is
// told with its next tool call or stop, or woken (deliver).
func (l *Loop) lapse(v host.View, st state.State) {
	p := st.OwnProposal()
	if l.Store.Worker() != "" || p == nil || p.Dismissed.IsZero() || p.Untimed != "" || p.Deadline.IsZero() ||
		st.Mode != state.ModeRunning || v.Now.Before(p.Deadline) || st.Run.Dialogs.Has("proposal") {
		return
	}
	var id string
	var n int
	var last bool
	l.update(func(st *state.State) {
		q := st.OwnProposal()
		if q == nil || q.ID != p.ID || q.Dismissed.IsZero() {
			return
		}
		id = q.ID
		n, last = state.WentAhead(st, q, l.MaxAuto, v.Now)
		phase := st.Current
		if last {
			phase = q.Phase
		}
		say, tell := decide.WentAhead(q.ID, q.What, q.Undo, phase, q.Deadline, last)
		st.Run.Announce = append(st.Run.Announce, say)
		state.Tell(st, state.Message{Kind: state.MessageAnswer, Text: tell}, v.Now)
	})
	if id == "" {
		return
	}
	l.Store.Event("proposal_resolved", map[string]any{"id": id, "by": state.ByTimeout, "answer": "Go ahead", "via": "lapsed"})
	if last {
		l.Store.Event("review_due", map[string]any{"phase": p.Phase, "decisions": n})
	}
}
