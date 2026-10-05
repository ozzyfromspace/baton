package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// newIdleRig is a running plan, mid-phase, with nothing owed: the watchdog's territory.
func newIdleRig(t *testing.T) *rig {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		state.Start(st, r.now, "")
		st.Run.Compaction = state.Compaction{}
		st.Run.LastActivity, st.Run.LastStop = r.now, r.now
	})
	r.tick(host.TickInterval) // first tick arms the clock
	return r
}

func TestIdleSessionIsRemindedThenEscalated(t *testing.T) {
	r := newIdleRig(t)
	for i := 0; i < 9; i++ { // ticks are continuous in real life: a single 9-minute gap would look like sleep
		r.tick(time.Minute)
	}
	if len(r.in.text) != 0 {
		t.Fatal("reminded too early")
	}
	// Ticks are continuous in real life; step in sub-ClockJump increments.
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], "No activity for 10m0s and P1 is not reported done") {
		t.Fatalf("reminder %q", r.in.text)
	}
	for i := 0; i < 11; i++ {
		r.tick(time.Minute)
	}
	if e := r.state().Run.Escalation; e == nil || !strings.Contains(e.Reason, "even after a reminder") {
		t.Fatalf("escalation %+v", e)
	}
	r.tick(time.Second)
	if len(r.push.kinds) != 1 || r.push.kinds[0] != "stalled" {
		t.Fatalf("push %v", r.push.kinds)
	}
	for i := 0; i < 30; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 {
		t.Fatalf("kept nudging while the human has it: %q", r.in.text)
	}
}

func TestActivityKeepsTheWatchdogQuiet(t *testing.T) {
	r := newIdleRig(t)
	for i := 0; i < 30; i++ {
		r.tick(time.Minute)
		r.set(func(st *state.State) { st.Run.LastActivity = r.now })
	}
	if len(r.in.text) != 0 {
		t.Fatalf("nudged an active session: %q", r.in.text)
	}
}

func TestDeclaredWaitAndBackgroundWorkHoldTheWatchdog(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { state.SetWaiting(st, "the deploy", 25*time.Minute, r.now) })
	for i := 0; i < 25; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 0 {
		t.Fatal("nudged during a declared wait")
	}
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], `wait for "the deploy" has run out`) {
		t.Fatalf("after the wait: %q", r.in.text)
	}

	r = newIdleRig(t)
	r.set(func(st *state.State) { st.Run.Background = []state.Task{{Type: "shell", Status: "running"}} })
	for i := 0; i < 29; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 0 {
		t.Fatal("nudged while background work runs")
	}
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 || !strings.Contains(r.in.text[0], "Background work has been running") {
		t.Fatalf("after background max: %q", r.in.text)
	}
}

func TestOpenDialogNotifiesTheHumanOnce(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { st.Run.Dialog = &state.Dialog{Tool: "Bash", Since: r.now} })
	for i := 0; i < 10; i++ {
		r.tick(time.Minute)
	}
	if len(r.push.kinds) != 1 || r.push.kinds[0] != "dialog" || len(r.in.text) != 0 {
		t.Fatalf("pushes %v typed %q", r.push.kinds, r.in.text)
	}
}

func TestRateLimitIsAnnouncedOnceAndRetriedWithBackoff(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: "rate_limit", Details: "resets at 3pm", At: r.now}
	})
	retries := func() int {
		n := 0
		for _, s := range r.in.text {
			if strings.Contains(s, "API error (rate_limit)") {
				n++
			}
		}
		return n
	}
	for i := 0; i < 14; i++ {
		r.tick(time.Minute)
	}
	if retries() != 0 || len(r.push.kinds) != 1 || r.push.kinds[0] != "rate_limit" {
		t.Fatalf("before the first retry: typed %q pushes %v", r.in.text, r.push.kinds)
	}
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if retries() != 1 {
		t.Fatalf("first retry: %q", r.in.text)
	}
	for i := 0; i < 28; i++ { // the second retry waits twice as long (30m after the first, which came at 15m)
		r.tick(time.Minute)
	}
	if retries() != 1 {
		t.Fatalf("no backoff: %q", r.in.text)
	}
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if retries() != 2 || len(r.push.kinds) != 1 {
		t.Fatalf("second retry: %q pushes %v", r.in.text, r.push.kinds)
	}
	// A turn that starts on its own supersedes the retries.
	r.set(func(st *state.State) { st.Run.TurnStarted = r.now; st.Run.LastActivity = r.now })
	for i := 0; i < 5; i++ {
		r.tick(time.Minute)
	}
	if retries() != 2 {
		t.Fatal("retried after the model resumed")
	}
}

func TestSleepRestartsTheIdleTimer(t *testing.T) {
	r := newIdleRig(t)
	r.tick(9 * time.Minute)
	r.tick(time.Hour) // the laptop slept for an hour
	r.tick(time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("nudged right after waking: %q", r.in.text)
	}
	for i := 0; i < 11; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 {
		t.Fatalf("no reminder after waking and staying idle: %q", r.in.text)
	}
}

// An error that will not pass on its own (an expired login, billing) is reported at once, never retried.
func TestPermanentAPIErrorsAreReportedNotRetried(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: "authentication_failed", Details: "please run /login", At: r.now}
	})
	r.run(2 * time.Hour)
	if len(r.in.text) != 0 {
		t.Fatalf("retried a permanent error: %q", r.in.text)
	}
	if e := r.state().Run.Escalation; e == nil || e.Kind != "api_error" || !strings.Contains(e.Reason, "please run /login") {
		t.Fatalf("escalation %+v", e)
	}
	if r.push.kinds[0] != "api_error" {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}

// Overloads are retried with backoff, and the human hears about them if they persist.
func TestPersistentOverloadIsReported(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { st.Run.LastError = &state.StopError{Error: "overloaded", At: r.now} })
	r.run(90 * time.Minute)
	if e := r.state().Run.Escalation; e == nil || e.Kind != "api_error" || !strings.Contains(e.Reason, "keeps retrying") {
		t.Fatalf("escalation %+v (typed %d)", e, len(r.in.text))
	}
	n := len(r.in.text)
	r.run(time.Hour)
	if len(r.in.text) <= n {
		t.Fatal("stopped retrying after reporting")
	}
}

// Reminders that get answered without the plan moving escalate instead of repeating forever.
func TestRemindersWithoutProgressEscalate(t *testing.T) {
	r := newIdleRig(t)
	for round := 0; round < MaxIdleNudges; round++ {
		r.run(DefaultTiming.IdleNudge + time.Minute)
		// The model answers the reminder (a turn, some activity) and stops again without progress.
		r.set(func(st *state.State) { st.Run.TurnStarted, st.Run.LastActivity, st.Run.LastStop = r.now, r.now, r.now })
		r.tick(time.Second)
	}
	if len(r.in.text) != MaxIdleNudges {
		t.Fatalf("reminders %q", r.in.text)
	}
	r.run(DefaultTiming.IdleNudge + time.Minute)
	if e := r.state().Run.Escalation; len(r.in.text) != MaxIdleNudges || e == nil || !strings.Contains(e.Reason, "has not moved") {
		t.Fatalf("typed %q escalation %+v", r.in.text, e)
	}
	// The plan moves: the count starts over.
	r.set(func(st *state.State) { st.Run.Progress() })
	if r.state().Run.IdleNudges != 0 || r.state().Run.Escalation != nil {
		t.Fatal("progress did not reset the reminders")
	}
}

// A dialog nobody answers is pushed again, not just once.
func TestUnansweredDialogIsPushedAgain(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { st.Run.Dialog = &state.Dialog{Tool: "AskUserQuestion", Since: r.now} })
	r.run(DefaultTiming.DialogNotify + Reminders[0] + time.Minute)
	if len(r.push.kinds) != 2 || r.push.kinds[1] != "dialog" {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}

// baton answers its own context question ("Keep going", option 2) when nobody has within WarnTimeout,
// so an unattended run does not stop on a warning. It never answers anything else, and never while
// the human is typing.
func TestTheContextQuestionTimesOut(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialog = &state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"}
	})
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("answered early: %q", r.in.text)
	}
	r.view.LastHumanKey = r.now.Add(2 * time.Minute) // the human is typing an answer
	r.run(90 * time.Second)
	if len(r.in.text) != 0 {
		t.Fatalf("answered over the human: %q", r.in.text)
	}
	r.run(3 * time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "2" {
		t.Fatalf("typed %q", r.in.text)
	}
	r.run(5 * time.Minute)
	if len(r.in.text) != 1 {
		t.Fatalf("answered twice: %q", r.in.text)
	}

	// Any other question waits for the human, however long.
	r = newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialog = &state.Dialog{Tool: "AskUserQuestion", Since: r.now}
	})
	r.run(DefaultTiming.WarnTimeout * 3)
	if len(r.in.text) != 0 {
		t.Fatalf("answered a question that was not baton's: %q", r.in.text)
	}
}
