// Package loop is the host's controller: on every tick it reads the project state the hooks keep, and
// it is the only part of baton that acts on its own. It has the lead compacted when a compaction is queued
// and it is safe to (by baton's mod where it runs, otherwise by typing /compact), demands proof the
// compaction happened, nudges a model that did not resume, and sends the human the notices the hooks
// queued. Where the mod runs, nothing is typed into the session at all (mod.go).
//
// Nothing here may wait forever. Every state baton waits in has a deadline, and every gate that holds
// baton back either expires (a screen that never settles, a turn with no sign of life) or is reported
// to the human with its reason.
//
// Each session of a run has a Loop of its own, and a Coordinator ticks them and does what belongs to the
// run as a whole.
package loop

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/notify"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// Timing is every deadline the loop enforces.
type Timing struct {
	Quiet          time.Duration // the screen must be still this long before baton types
	QuietMax       time.Duration // a screen that never settles (a status line redrawn every second) is ignored after this
	HandsOff       time.Duration // and the human must not have typed for this long
	AckTimeout     time.Duration // a compaction must show it began (the mod's report) this soon after typing /compact (where the mod compacts, its ack says)
	CompactTimeout time.Duration // and its proof must come this soon after it began
	FailedRetry    time.Duration // a failed compaction is tried again after this, then 3× longer each round
	FailedRetryMax time.Duration // but never further apart than this: it is tried again for as long as it fails
	StalledNudge   time.Duration // a session the watchdog escalated as stalled is still reminded this often
	ResumeNudge    time.Duration // no turn this long after a compaction: type a reminder
	ResumeEscalate time.Duration // still nothing this long after the reminder: bring the human in
	DraftGrace     time.Duration // a draft holding baton this long is saved and cleared, and baton carries on
	RescueBackoff  time.Duration // and the box is not cleared again within this, however the flag reads
	DraftEscalate  time.Duration // a human draft blocking baton this long: tell them
	StaleTurn      time.Duration // an open turn with no hook activity this long is not trusted to be open
	IdleNudge      time.Duration // no hook activity this long mid-plan: type a reminder (then escalate)
	BackgroundMax  time.Duration // background subagents may hold a compaction this long
	BackgroundIdle time.Duration // the model's own background work may keep a quiet session from reminders this long
	DialogNotify   time.Duration // a permission prompt or question open this long: notify the human
	WarnTimeout    time.Duration // the context question unanswered this long: baton answers "Keep going"
	RateLimitRetry time.Duration // retry interval after a usage-limit error (doubled once at most)
	OverloadRetry  time.Duration // first retry after an overload or server error (then backoff)
	RecoverAfter   time.Duration // past state.MaxRecoveries in state.RecoveryWindow, a lead or an agent that stopped by nobody's choice is brought back after this (state.Backoff)
	RecoverEvery   time.Duration // and then this long apart, for as long as it keeps stopping
	ClockJump      time.Duration // a gap between ticks this long means the machine slept
	NoticeRetry    time.Duration // a notice that failed to send is retried after this
	RestartIdle    time.Duration // with no plan running, a session idle this long may restart on a newer baton
	JobStale       time.Duration // a running job whose runner has not beaten for this long is taken for dead
	SpawnGrace     time.Duration // a runner gone this long after it was started, without taking up its job, never will
	SwitchAfter    time.Duration // the phase in hand waiting this long on its job is set aside for another (switch_after)
	AgentStart     time.Duration // baton's mod must have started a phase agent this soon after it was asked
	ModHello       time.Duration // baton's mod must say hello this soon after a plan runs in the lead's session, or the run is serial
	StallAfter     time.Duration // nothing in a running plan moved for this long, and nothing busy: tell the human (stall.go; 0: never)
	SlowAfter      time.Duration // a gate running past 1.5× its command's slowest earlier pass, and this much more, is a look-in for its owner (lookin.go; 0: never)
	HeldAfter      time.Duration // landings waiting this long behind a gate in the lead's checkout are a look-in for the lead (lookin.go; 0: never)
	JobTimeout     time.Duration // verify_timeout: a gate at 80% of it is a look-in for its owner (lookin.go; 0: never)
	HoldLapse      time.Duration // a proposal held for the human (Wait for me) with no word from them this long: baton lifts the hold (lapseHold)
	BlockLook      time.Duration // a block with no word from the human this long: baton lifts it, and the model looks again (block.go; 0: decide.BlockLook)
	LandNudge      time.Duration // a landing the lead's uncommitted changes have held this long: the lead's model is asked to commit them (merge.go)
	HungAfter      time.Duration // a running gate whose log has not grown, and whose processes used no CPU, for this long is stopped as hung (jobs.go; 0: never)
	ReplanMax      time.Duration // a replan of the phases not started open this long lapses: its draft is dropped, and the plan goes on as it was (replanmax.go; 0: never)
	AdvisorWait    time.Duration // a look-in held for the advisor's note goes without it this long after it was held, whatever the advisor does (advisor.go; 0: AdvisorWait)

	// EscalationTimeout is how long a proposal waits for the human before baton goes ahead with it
	// (escalation_timeout); a key the human presses starts it over.
	EscalationTimeout time.Duration
}

// DefaultTiming is baton's production timing. Nothing baton can decide waits on the human for longer
// than 20 minutes (most for 10): a question gets its default at escalation_timeout, and a hold the human
// chose lapses after HoldLapse. A block, which the model judged only the human could answer, is lifted
// after BlockLook for it to look again. The rest is sized to what it waits for: a turn, a compaction, the
// mod, a subagent, a reply over the network.
var DefaultTiming = Timing{
	Quiet: 1500 * time.Millisecond, QuietMax: 30 * time.Second, HandsOff: 3 * time.Second,
	AckTimeout: 15 * time.Second, CompactTimeout: 10 * time.Minute, FailedRetry: 5 * time.Minute, FailedRetryMax: 15 * time.Minute, StalledNudge: 15 * time.Minute,
	ResumeNudge: 60 * time.Second, ResumeEscalate: 3 * time.Minute,
	DraftGrace: 20 * time.Second, RescueBackoff: 2 * time.Minute, DraftEscalate: 2 * time.Minute,
	StaleTurn: 15 * time.Minute, IdleNudge: 10 * time.Minute, BackgroundMax: 20 * time.Minute, BackgroundIdle: 20 * time.Minute,
	DialogNotify: 3 * time.Minute, WarnTimeout: valve.DefaultWarnTimeout, RateLimitRetry: 15 * time.Minute, OverloadRetry: time.Minute,
	RecoverAfter: 5 * time.Minute, RecoverEvery: 15 * time.Minute,
	ClockJump: 2 * time.Minute, NoticeRetry: time.Minute, RestartIdle: 2 * time.Minute, JobStale: 2 * time.Minute, SpawnGrace: 30 * time.Second,
	SwitchAfter: config.DefaultSwitchAfter, AgentStart: 5 * time.Minute, ModHello: 15 * time.Second, StallAfter: 20 * time.Minute,
	SlowAfter: 15 * time.Minute, HeldAfter: 20 * time.Minute, JobTimeout: config.DefaultVerifyTimeout,
	HoldLapse: decide.HoldLapse, BlockLook: decide.BlockLook, LandNudge: 10 * time.Minute, HungAfter: 20 * time.Minute,
	ReplanMax:         replan.Max,
	AdvisorWait:       AdvisorWait,
	EscalationTimeout: config.DefaultEscalationTimeout,
}

// MaxTries is how many times baton asks for a compaction in one round (typing /compact, or by its mod)
// before it gives up on the round.
const MaxTries = 2

// MaxRounds is how many rounds of attempts a failed compaction gets before baton says it keeps trying
// every FailedRetryMax: nothing gives up on it, since the run waits on it.
const MaxRounds = 3

// MaxIdleNudges is how many reminders baton types without the plan moving before it brings the human in.
const MaxIdleNudges = 2

// Reminders are when an unresolved escalation is pushed again, measured from when it was raised. Twice is
// enough: by then a question of baton's has had its default (escalation_timeout), and a hold has lapsed
// (Timing.HoldLapse), and what still waits on the human is told on the board and by the stall alarm.
var Reminders = []time.Duration{10 * time.Minute, 20 * time.Minute}

// clearLine is sent before retrying: Ctrl-U ×6 wipes whatever is left of baton's own failed attempt.
// It is only sent when the human has no draft (a gate), so it never erases their text, and only where
// baton types (serial mode).
const clearLine = "\x15\x15\x15\x15\x15\x15"

// Loop implements host.Controller, for the lead session. (A phase agent's loop, Agent, keeps one for the
// duties it shares with the lead: its jobs, their settling, its notices and the snapshot of a halt.)
type Loop struct {
	Store   *state.Store
	Notify  notify.Notifier
	Project string
	Timing  Timing
	Logf    func(string, ...any)
	// Snapshot saves uncommitted work (gitx.Snapshot when nil).
	Snapshot func(root string, at time.Time, phase, why string) (gitx.Saved, error)
	// Version is the baton this session runs, and Newer reports a newer one it may restart on ("" if
	// none; upgrade.Watch). A nil Newer never restarts.
	Version string
	Newer   func() string
	// Runners runs the session's jobs (nil: jobs stay queued).
	Runners Runners
	// RunUsage is the run's account usage, its limit and its resume timer, for a phase agent's Loop, whose
	// sub-run does not record them as the lead's run does, and RunPaused whether the lead's run is paused
	// (the coordinator's; nil for the lead).
	RunUsage  func() *state.Usage
	RunPaused func() bool
	// Workers says the run has workers beside the lead (the coordinator's Fleet): while the lead is
	// stopped, the phases that do not need its phase go on.
	Workers bool
	// MaxAuto is max_auto_decisions: the decisions a phase may make without the human before they review
	// them (0: no cap, the default).
	MaxAuto int
	// Lapse answers a question of the lead's own, baton's, for the human (hooks.Lapse): one past its
	// deadline with no dialog of it on screen. Nil: such a question waits.
	Lapse func(s *state.Store, question, answer string, now time.Time) error
	// StartsAfresh says the host starts the lead afresh once the loop ends its claude for it (it execs a
	// new host: upgrade.CanRestart), after an API error a fresh session avoids (afresh.go). Without it, such
	// an error is the human's, as any other baton cannot retry away.
	StartsAfresh bool

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

	owner      bool      // this session has owned the project
	settled    time.Time // when the run's jobs were last settled
	retaken    time.Time // when a phase's sub-run last looked for its phase to take up again (retake)
	switchNext time.Time // when a switch may be tried next
	switchHeld string    // the job whose switch uncommitted work holds, reported once
	typedFor   string    // the jobs whose results baton last typed a line for, and when
	typedAt    time.Time
	quit       bool           // claude was asked to end: the human is leaving baton, or baton restarts
	restart    *restart       // the restart claude was ended for
	fresh      *fresh         // the fresh session claude was ended for (afresh.go)
	freshSeen  time.Time      // the error a fresh session was last found owed for
	halted     bool           // the run is halted, and this halt's snapshot was started
	snapping   atomic.Bool    // a snapshot is running
	saving     sync.WaitGroup // and Ended waits for it

	// When the running gates were last looked at for signs of life (hung), and what was seen of each, by job.
	hungAt time.Time
	lives  map[string]life

	// How the loop reaches the lead's claude (mod.go): whether its mod has said hello, which carries what
	// baton gives the lead from then on, and how this tick does (modMode), last looked at modAt, with the
	// host's first tick at hostAt; the line the mod was given and has not acked yet, given at submittedAt;
	// and when Claude Code last refused a compaction as a turn was running.
	modOn       bool
	mode        int
	modAt       time.Time
	hostAt      time.Time
	submitting  int
	submittedAt time.Time
	declinedAt  time.Time

	lastTick       time.Time
	wokeAt         time.Time
	idleNudged     time.Time
	dialogNotified time.Time
	rateNotified   bool
	errNudged      time.Time
	errRetries     int

	// advise is the advisor's part of the lead's tick, the coordinator's (advisor.go; nil: none): it holds
	// and releases the run's look-ins, so it comes before anything is delivered, and reports whether it
	// changed the run's state.
	advise func(now time.Time, s *state.Store, st state.State) bool
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
	if st.Owner != nil && !st.IsOwner(l.Store.Instance, v.Now) {
		return // another terminal drives this run now
	}
	l.mode = l.modMode(v)
	l.sendNotices(v, st)
	l.saveWork(v, st)
	if !st.Run.ExitAt.IsZero() {
		l.leave(v, st, in)
		return
	}
	if l.quit {
		return // claude is on its way out; nothing more may be typed into it
	}
	l.jobs(v, st)
	st = l.settle(v, st)
	if l.advise != nil && l.advise(v.Now, l.Store, st) {
		if fresh, err := l.Store.Load(); err == nil {
			st = fresh // a look-in held for the advisor is not typed for (deliver)
		}
	}
	l.lapseReplan(v, st)
	if st.Mode == state.ModeComplete && len(st.Due()) > 0 {
		// A verification completed the plan while the session was idle: it is told, and sums the run up.
		l.why, l.wished = l.gate(v, st), false
		l.clearDraft(v, st, in)
		l.deliver(v, st, in)
		l.publishGate(st)
		return
	}
	if st.Mode != state.ModeRunning {
		if l.upgradeDue(v, st) && l.restartable(v, st, l.gate(v, st)) {
			l.restartOn(v, st, in, "")
		}
		return
	}
	l.why = l.gate(v, st)
	l.wished = false
	l.resolve(v, st)
	l.remind(v, st)
	st = l.switching(v, st)
	l.clearDraft(v, st, in)
	if l.idleRestart(v, st, in) {
		return // the lead holds no phase, so no compaction comes to restart at (upgradeidle.go)
	}

	comp := st.Run.Compaction
	switch comp.Status {
	case state.CompactQueued:
		// The point a running plan restarts on a newer baton while its lead has a phase: a compaction is
		// due, and the restarted session types it instead. Nothing else in a running plan waits for a
		// restart.
		if l.upgradeDue(v, st) && l.restartable(v, st, l.why) {
			l.restartOn(v, st, in, "")
			return
		}
		l.moveAside(st) // before the compaction would add the plan-mode file to the context once more
		l.typeCompact(v, st, in)
	case state.CompactTyped:
		if comp.Command != 0 {
			l.asked(v, st) // the mod was asked for it
			break
		}
		switch since := v.Now.Sub(l.since(comp.Typed)); {
		case st.Run.ModReports.IsZero():
			// Nothing reports a compaction as it begins (the mod moves it to active at once where it runs):
			// only its proof says it happened, when it is over. Typing /compact again before then could
			// queue a second.
			if since > l.Timing.CompactTimeout {
				l.fail(comp, fmt.Sprintf("no proof came within %s that the compaction happened", l.Timing.CompactTimeout))
			}
		case since > l.Timing.AckTimeout:
			if comp.Tries < MaxTries {
				l.update(func(st *state.State) { st.Run.Compaction.Status = state.CompactQueued })
				l.Store.Event("compact_retry", map[string]any{"epoch": comp.Epoch, "tries": comp.Tries})
			} else {
				l.fail(comp, fmt.Sprintf("typed /compact %d times but it never started", comp.Tries))
			}
		}
	case state.CompactActive:
		if why := comp.Refused(); why != "" {
			l.redo(v, comp, why) // it did not happen after all: a hook vetoed it, or it failed
		} else if v.Now.Sub(l.since(comp.Started)) > l.Timing.CompactTimeout {
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

// clearDraft makes sure a draft can never hold a run. If one is in the way of something baton needs to
// type, it is saved where `baton drafts` hands it back, and the box cleared.
func (l *Loop) clearDraft(v host.View, st state.State, in host.Injector) {
	if l.why == gateDraft && l.owed(st) && l.rescueDraft(v, st, in) {
		// The box is empty as of a moment ago; the host's next view will say so too. Re-gate without
		// it so the work baton was holding goes out on this tick rather than the next.
		v.Draft, v.DraftText = false, ""
		l.why = l.gate(v, st)
	}
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
	if l.quit || st.Run.TurnOpen && !l.turnStale(v, st) || l.dialogHolds(st) || v.Now.Sub(v.LastOutput) < l.Timing.Quiet {
		return
	}
	l.quit = true
	l.Store.Event("left_baton", nil)
	if err := in.Quit(); err != nil {
		l.logf("loop: quitting: %v", err)
	}
}

// restart is the newer baton the loop ended claude for, and the conversation to resume on it.
type restart struct{ to, session string }

// Restart reports that the loop ended claude to restart the session on baton `to`, resuming `session`.
func (l *Loop) Restart() (to, session string, ok bool) {
	if l.restart == nil {
		return "", "", false
	}
	return l.restart.to, l.restart.session, true
}

// upgradeDue reports a newer baton to restart the lead on, for a session baton knows the conversation of.
func (l *Loop) upgradeDue(v host.View, st state.State) bool {
	return l.Newer != nil && st.Run.SessionID != "" && l.Newer() != ""
}

// restartable reports a safe point to restart the session: the moment is the human's and the model's
// to lose nothing in. Nothing may be on its way (a turn, a dialog, a compaction), and nothing that lives
// only inside this claude process may be running, because it would end with it: background work of any
// kind (baton's phase agents too), monitors, session-scoped wakeups. baton's own jobs do not count: their runners outlive the
// restart, and the restarted host watches them. With a plan running, the safe point is a compaction baton is
// about to type, or a lead idle with no phase and no phase agent live (idleRestart), decided by the caller;
// otherwise the session must have been idle for RestartIdle, so a human reading its last answer is not
// interrupted. While a newer baton waits and the lead can reach its safe point by itself, no phase agent
// starts (Coordinator.pending, upgradeHolds); one that is idle when the restart comes is taken up again
// after it.
func (l *Loop) restartable(v host.View, st state.State, why string) bool {
	if why != "" || st.Run.Crons > 0 || len(st.Run.BusyBackground()) > 0 || len(st.PhaseAgents()) > 0 || len(st.Run.Watching()) > 0 {
		return false
	}
	switch st.Run.Compaction.Status {
	case state.CompactTyped, state.CompactActive:
		return false
	}
	if st.Mode != state.ModeRunning {
		if idle := latest(v.LastHumanKey, st.Run.LastStop, st.Run.LastActivity, st.Run.TurnStarted); v.Now.Sub(idle) < l.Timing.RestartIdle {
			return false
		}
	}
	return true
}

// restartOn ends claude so the host can restart the session on the newer baton: the host's command
// resumes the same conversation on it, and the restarted session says so (Runtime.Restart). why, when
// set, says why it restarts where it does, for the log.
func (l *Loop) restartOn(v host.View, st state.State, in host.Injector, why string) {
	to := l.Newer()
	l.quit = true
	l.restart = &restart{to: to, session: st.Run.SessionID}
	l.update(func(st *state.State) { st.Run.Restart = &state.Restart{From: l.Version, To: to, At: v.Now} })
	fields := map[string]any{"from": l.Version, "to": to, "session": st.Run.SessionID, "mode": st.Mode}
	if why != "" {
		fields["why"] = why
	}
	l.Store.Event("restarting", fields)
	l.logf("loop: restarting the session on baton %s", to)
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
		moved(l.Store, s, l.logf)
	}
	l.Store = s
	l.halted, l.staleLog, l.dialogLog, l.idleNudged, l.dialogNotified = false, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	l.settled, l.typedFor, l.typedAt, l.switchNext, l.switchHeld = time.Time{}, "", time.Time{}, time.Time{}, ""
	l.rateNotified, l.errNudged, l.errRetries, l.freshSeen = false, time.Time{}, 0, time.Time{}
	l.modAt, l.submitting, l.declinedAt = time.Time{}, 0, time.Time{} // the line open was the other run's bridge's
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
		l.wokeAt, l.hostAt = v.Now, v.Now
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
	gateUsage  = "the account waits on its usage limit"
	gateMod    = "baton's mod has not said hello yet"
)

// gate says why baton must not type right now, or "" if it may. It runs once per tick.
//
// Two gates expire instead of holding forever. A turn that shows no sign of life for StaleTurn is not
// trusted to be open: the Stop hook does not run when a turn is interrupted, and a hook can fail.
// (Claude Code's idle notification usually corrects the flag within a minute; this is the backstop.)
// And a screen that never stays still for Quiet, while everything else allows typing, is ignored after
// QuietMax: something is redrawing it on a timer, such as a status line with a clock.
//
// Where baton's mod runs, nothing is typed, so neither the human's draft nor the screen holds baton: the
// mod compacts the lead and submits baton's lines with the draft left in the box (spike 35). A turn, a
// dialog of the lead's (dialogHolds), the usage limit and the human typing this moment still do. Until the
// mod in a restarted claude says hello (modWaiting), nothing goes out. A phase agent's loop types nothing
// either (agentLine): its lines are messages the mod sends (line.go), which Claude Code holds for the
// agent's next tool round, and it has no screen to settle.
func (l *Loop) gate(v host.View, st state.State) string {
	why := ""
	switch {
	case st.Run.TurnOpen && !l.turnStale(v, st):
		why = gateTurn
	case l.dialogHolds(st):
		why = gateDialog
	case v.Now.Before(l.limited(st)):
		why = gateUsage // anything typed would fail on it; once it ends, each stopped session's own retry goes (watchError)
	case l.mode == modWaiting:
		why = gateMod
	case busyAgents(st) && !l.agentsStale(v, st):
		why = gateAgents
	case v.Draft && l.mode != modOn:
		why = gateDraft
	case v.Now.Sub(v.LastHumanKey) < l.Timing.HandsOff:
		why = gateTyping
	}
	if why != "" {
		l.openSince = time.Time{}
		return why
	}
	if l.mode == modOn || l.agentLine() {
		return ""
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

// dialogHolds reports a dialog open that holds the lead: its compaction, its deliveries, a switch and
// leaving baton. Where baton's mod runs, a phase agent's dialog holds only that agent (state.LeadOpen):
// the mod's compaction, the rewake after it and a line it submits all run under its permission prompt,
// which survives them, and the lead's own call that needs one waits behind it (spike 36). Anywhere else
// every dialog holds: a key baton types lands in the one on screen, and Enter in a permission prompt
// approves the call.
func (l *Loop) dialogHolds(st state.State) bool {
	if l.mode == modOn {
		return st.Run.Dialogs.LeadOpen()
	}
	return st.Run.Dialogs.AnyOpen()
}

// limited is until when the session's run waits on the account's usage limit (zero: it does not).
func (l *Loop) limited(st state.State) time.Time {
	var until time.Time
	if st.Usage != nil {
		until = st.Usage.Limited
	}
	if l.RunUsage != nil {
		if u := l.RunUsage(); u != nil {
			until = latest(until, u.Limited)
		}
	}
	return until
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
// that waits for an answer is working as intended, and that wait is escalated and pushed. Where baton's
// mod runs nothing is typed, and a phase agent's prompt holds only that agent (dialogHolds).
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

// owed reports work baton must type to make progress: a compaction at a boundary or a checkpoint, or the
// results of jobs the model waits on. A nudge is deliberately not owed — a reminder arriving late costs
// nothing, and clearing somebody's input box to send one would be rude.
func (l *Loop) owed(st state.State) bool {
	switch st.Run.Compaction.Status {
	case state.CompactQueued, state.CompactFailed:
		return true
	}
	return st.BoundaryOwed || st.CheckpointOwed || len(st.Due()) > 0
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
//
// Ctrl-C is how it clears the box, and one Ctrl-C at an idle prompt stops every background agent of the
// session for good, baton's and the model's own (spike 35): it never runs while one may be live
// (agentsLive), and the draft holds baton, which says so (held). Where baton's mod runs it never runs at
// all: nothing there waits on a draft.
func (l *Loop) rescueDraft(v host.View, st state.State, in host.Injector) bool {
	if l.viaMod() {
		return false
	}
	// Something wants to type, which is what keeps heldSince from being reset at the end of the tick.
	l.wished = true
	if agentsLive(st) {
		return false
	}
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

// agentsLive reports an agent that may be at work in the lead's claude: a phase agent on the run's roster,
// or a background agent of the lead's model (a subagent, a workflow, a teammate), as its last stop listed
// them. One Ctrl-C at the prompt stops every one of them for good (spike 35).
func agentsLive(st state.State) bool {
	for _, w := range st.LiveWorkers() {
		if w.AgentID != "" {
			return true
		}
	}
	for _, t := range st.Run.Background {
		switch {
		case t.AgentType == state.PhaseAgent, t.Type == "subagent", t.Type == "workflow", t.Type == "teammate":
			return true
		}
	}
	return false
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
		if agentsLive(st) {
			reason = decide.DraftHolds(task)
		}
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
	if l.viaMod() {
		if v.Now.Sub(l.declinedAt) < l.Timing.HandsOff {
			return // refused a moment ago as a turn ran: the turn's hooks say so first
		}
		l.askCompact(v, st)
		return
	}
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

// retryFailed starts a failed compaction over, with backoff, for as long as it fails. It stays owed (the
// model is held until it happens), so giving up would park the run for good.
func (l *Loop) retryFailed(v host.View, st state.State) {
	comp := st.Run.Compaction
	if !(st.BoundaryOwed || st.CheckpointOwed) {
		return
	}
	wait := l.Timing.FailedRetry
	for i := 0; i < comp.Rounds && wait < l.Timing.FailedRetryMax; i++ {
		wait *= 3
	}
	if l.Timing.FailedRetryMax > 0 {
		wait = min(wait, l.Timing.FailedRetryMax)
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

// checkResumed makes sure the model actually started working after a compaction baton asked for: it is
// reminded, then the human is brought in, and the model is still reminded every StalledNudge after that,
// as a session that went quiet is (watch), so that it picks the plan up again by itself.
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
		l.resumeNudge(v, st, in, false)
	case !comp.Nudged.IsZero() && v.Now.Sub(l.since(comp.Nudged)) > l.Timing.ResumeEscalate:
		if e := st.Run.Escalation; e == nil || e.Kind != "stalled" {
			l.escalate("stalled", fmt.Sprintf("the model did not resume %s after the compaction", st.Current))
		} else if v.Now.Sub(l.since(comp.Nudged)) >= l.Timing.StalledNudge {
			l.resumeNudge(v, st, in, true)
		}
	}
}

// resumeNudge reminds the model to take up the phase in hand after a compaction baton asked for. stalled
// says the human was brought in already: what holds the reminder back then is not escalated over it.
func (l *Loop) resumeNudge(v host.View, st state.State, in host.Injector, stalled bool) {
	comp := st.Run.Compaction
	if l.why != "" {
		if !stalled {
			l.held(v, st, "remind the model to resume after the compaction")
		}
		return
	}
	l.proceed()
	verb := "Begin"
	switch {
	case comp.Reason == "checkpoint":
		verb = "Continue"
	case st.Resumed():
		verb = "Resume"
	}
	msg := fmt.Sprintf("[baton] Context compacted. %s %s now — the brief above has your instructions.", verb, st.Current)
	if err := l.say(v, in, msg); err != nil {
		l.unsaid(err, "reminding the model to resume")
		return
	}
	l.update(func(st *state.State) { st.Run.Compaction.Nudged = v.Now })
	fields := map[string]any{"epoch": comp.Epoch, "via": l.via()}
	if stalled {
		fields["stalled"] = true
	}
	l.Store.Event("resume_nudge", fields)
}

func (l *Loop) fail(comp state.Compaction, reason string) {
	l.update(func(st *state.State) {
		st.Run.Compaction.Status, st.Run.Compaction.FailedAt, st.Run.Compaction.Declined = state.CompactFailed, l.Store.Now(), nil
	})
	l.Store.Event("compact_failed", map[string]any{"epoch": comp.Epoch, "round": comp.Rounds + 1, "reason": reason})
	if comp.Rounds+1 < MaxRounds {
		reason += "; baton will try again"
	} else {
		reason += fmt.Sprintf("; baton keeps trying every %s (or type /compact yourself once the turn is over)", decide.Spell(l.Timing.FailedRetryMax))
	}
	l.escalate("compaction_failed", reason)
}

// escalate records a watchdog escalation and queues a notice; the next tick sends it.
func (l *Loop) escalate(kind, reason string) { l.escalateWith(kind, reason, nil) }

// escalateWith is escalate, with more fields for its event.
func (l *Loop) escalateWith(kind, reason string, fields map[string]any) {
	now := l.Store.Now()
	l.update(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Kind: kind, Reason: reason, Since: now, Watchdog: true, LastPush: now}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: kind, Text: reason, At: now})
	})
	ev := map[string]any{"type": kind, "reason": reason}
	for k, v := range fields {
		ev[k] = v
	}
	l.Store.Event("escalated", ev)
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
	case "api_error", "refused":
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
// easy to miss, and a run waiting on the human loses hours until they see it. A hold for the human is
// not pushed as still waiting once it would have lapsed (Timing.HoldLapse): the push that it lapsed says
// what became of it.
func (l *Loop) remind(v host.View, st state.State) {
	e := st.Run.Escalation
	if e == nil || e.Reminded >= len(Reminders) || v.Now.Sub(e.Since) < Reminders[e.Reminded] ||
		e.Kind == "decision" && l.Timing.HoldLapse > 0 && Reminders[e.Reminded] >= l.Timing.HoldLapse {
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
