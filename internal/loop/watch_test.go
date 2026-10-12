package loop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// newIdleRig is a running plan, mid-phase, with nothing owed: the watchdog's territory.
func newIdleRig(t *testing.T) *rig {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.BoundaryOwed = false
		state.Start(st, r.now, state.Origin{})
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

// The model's own background work keeps reminders away until the oldest of it has run for BackgroundIdle,
// counted from when a Stop first reported it. Then baton looks in, and reminders that change nothing bring
// the human in.
func TestBackgroundWorkHoldsTheWatchdogForAnHour(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Background = []state.Task{{ID: "b1", Type: "shell", Status: "running", Since: r.now}}
	})
	for i := 0; i < 20; i++ {
		r.tick(time.Minute)
	}
	r.set(func(st *state.State) { // the model stops on it again, and starts more
		st.Run.Background = append(st.Run.Background, state.Task{ID: "b2", Type: "shell", Since: r.now})
		st.Run.LastActivity, st.Run.LastStop = r.now, r.now
	})
	for i := 0; i < 39; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 0 {
		t.Fatal("nudged while background work runs")
	}
	for i := 0; i < 2; i++ {
		r.tick(time.Minute)
	}
	if len(r.in.text) != 1 || r.in.text[0] != "[baton] Background work has been running for over 60 minutes with no progress on P1. Check on it, then continue, or report your status." {
		t.Fatalf("after an hour: %q\n%s", r.in.text, r.events())
	}
	// The model answers each reminder and stops on the work again: one more reminder, then the human.
	for n := 1; n <= 2; n++ {
		r.tick(time.Second)
		r.set(func(st *state.State) { st.Run.LastActivity, st.Run.LastStop = r.now, r.now })
		for i := 0; i < 11; i++ {
			r.tick(time.Minute)
		}
	}
	if e := r.state().Run.Escalation; len(r.in.text) != 2 || e == nil || e.Reason != "P1 has not moved after 2 reminders" {
		t.Fatalf("typed %q, escalation %+v", r.in.text, e)
	}
}

// Work a Stop has not reported a start for counts from the last Stop.
func TestBackgroundSince(t *testing.T) {
	stop := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		since, want time.Time
	}{{stop.Add(-time.Hour), stop.Add(-time.Hour)}, {time.Time{}, stop}, {stop.Add(time.Minute), stop}} {
		if got := backgroundSince([]state.Task{{ID: "b1", Type: "shell", Since: c.since}}, stop); !got.Equal(c.want) {
			t.Errorf("since %s: %s, want %s", c.since, got, c.want)
		}
	}
}

func TestOpenDialogNotifiesTheHumanOnce(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Since: r.now}) })
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
	if e := r.state().Run.Escalation; e == nil || e.Kind != "api_error" || !strings.Contains(e.Reason, "please run /login") ||
		!strings.Contains(e.Reason, "Only you can decide how P1 goes on; nothing else runs until then") {
		t.Fatalf("escalation %+v", e)
	}
	if r.push.kinds[0] != "api_error" {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}

// With workers, the escalation for an error baton cannot retry says what goes on without the lead, and
// which phases wait for its phase.
func TestPermanentAPIErrorSaysWhatWaits(t *testing.T) {
	r := newIdleRig(t)
	r.ctl, r.loop.Workers = r.loop, true // the lead's Loop, as a coordinator with a fleet ticks it
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"},
		{ID: "P2", Title: "c", After: []string{"P1"}}, {ID: "P3", Title: "d", After: []string{"P0"}}, {ID: "P4", Title: "e", After: []string{"P2"}}}}
	if err := r.loop.Store.SavePlan(pl); err != nil {
		t.Fatal(err)
	}
	r.set(func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: "invalid_request", At: r.now}
	})
	r.tick(10 * time.Second)
	want := "P1 stopped: the last turn failed with an API error baton cannot retry away (invalid_request). Only you can decide how P1 goes on; " +
		"meanwhile workers carry on with every phase that does not need P1. P2 and P4 wait for it."
	if e := r.state().Run.Escalation; e == nil || e.Reason != want {
		t.Fatalf("escalation %+v", e)
	}
}

// A turn the model's safeguards declined is escalated at once as a refusal, with what only the human
// decides, and baton never retries it: nothing is typed into that conversation, however long it waits.
// The human is reminded on the usual schedule, and a later turn that ends well clears it.
func TestARefusalIsEscalatedAndNeverRetried(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: state.ErrRefusal, Category: "cyber", Details: "declined", At: r.now}
	})
	r.tick(10 * time.Second)
	want := "P1 stopped: the model's safeguards declined the last turn (cyber), and this conversation will not go on as it is. baton does not retry it. " +
		"To go on, rewind (Esc Esc) to before the flagged turn and take a different approach, as Claude Code advises, or /clear for a fresh conversation " +
		"from P1's brief. Nothing else runs until then."
	if e := r.state().Run.Escalation; e == nil || e.Kind != "refused" || e.Reason != want {
		t.Fatalf("escalation %+v", e)
	}
	if ev := r.events(); !strings.Contains(ev, `"kind":"escalated"`) || !strings.Contains(ev, `"type":"refused"`) || !strings.Contains(ev, `"category":"cyber"`) {
		t.Fatalf("events:\n%s", ev)
	}
	for range 2*60 + 10 { // past the second reminder, in ticks short enough not to look like a sleep
		r.tick(time.Minute)
	}
	if len(r.in.text) != 0 || r.in.quit != 0 {
		t.Fatalf("typed into a refused conversation: %q (quit %d)", r.in.text, r.in.quit)
	}
	if n := strings.Count(r.events(), `"type":"refused"`); n != 3 {
		t.Fatalf("escalated and reminded %d times, want once and 2 reminders:\n%s", n, r.events())
	}
	if len(r.push.kinds) != 3 || r.push.kinds[0] != "refused" {
		t.Fatalf("pushes %v", r.push.kinds)
	}

	// The human rewound and wrote, and the turn ended well.
	r.set(func(st *state.State) {
		st.Run.TurnStarted, st.Run.LastStop, st.Run.LastActivity = r.now, r.now.Add(time.Minute), r.now
	})
	r.tick(2 * time.Minute)
	if e := r.state().Run.Escalation; e != nil {
		t.Fatalf("still escalated after a turn that ended well: %+v", e)
	}
	if !slices.ContainsFunc(strings.Split(r.events(), "\n"), func(l string) bool {
		return strings.Contains(l, `"kind":"escalation_cleared"`) && strings.Contains(l, `"type":"refused"`)
	}) {
		t.Fatalf("events:\n%s", r.events())
	}
}

// An escalation of another API error gives way to a refusal that came after it, and a refusal open is
// not escalated again on every tick.
func TestARefusalAfterAnotherErrorIsEscalatedAsOne(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: "invalid_request", At: r.now}
	})
	r.tick(10 * time.Second)
	if e := r.state().Run.Escalation; e == nil || e.Kind != "api_error" {
		t.Fatalf("escalation %+v", e)
	}
	r.set(func(st *state.State) {
		st.Run.TurnStarted = r.now
		st.Run.LastError = &state.StopError{Error: state.ErrRefusal, At: r.now.Add(time.Second)}
	})
	r.run(time.Minute)
	if e := r.state().Run.Escalation; e == nil || e.Kind != "refused" || !strings.Contains(e.Reason, "declined the last turn, and") {
		t.Fatalf("escalation %+v", e)
	}
	if n := strings.Count(r.events(), `"kind":"escalated"`); n != 2 {
		t.Fatalf("%d escalations:\n%s", n, r.events())
	}
}

// With workers, a refusal of the lead's turn says what goes on without it and which phases wait.
func TestARefusalSaysWhatWaits(t *testing.T) {
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a", After: []string{}}, {ID: "P1", Title: "b", After: []string{}},
		{ID: "P2", Title: "c", After: []string{"P1"}}, {ID: "P3", Title: "d", After: []string{"P2"}}}}
	refused := func(st *state.State, at time.Time) {
		st.Run.LastError = &state.StopError{Error: state.ErrRefusal, Category: "cyber", At: at}
	}
	t.Run("the lead", func(t *testing.T) {
		r := newIdleRig(t)
		r.ctl, r.loop.Workers = r.loop, true
		if err := r.loop.Store.SavePlan(pl); err != nil {
			t.Fatal(err)
		}
		r.set(func(st *state.State) { refused(st, r.now) })
		r.tick(10 * time.Second)
		if e := r.state().Run.Escalation; e == nil || !strings.HasSuffix(e.Reason, "from P1's brief. Meanwhile workers carry on with every phase that does not need P1. P2 and P3 wait for it.") {
			t.Fatalf("escalation %+v", e)
		}
	})
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
	r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now}) })
	r.run(DefaultTiming.DialogNotify + Reminders[0] + time.Minute)
	if len(r.push.kinds) != 2 || r.push.kinds[1] != "dialog" {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}

// baton answers its own context question ("Keep going", option 3) when nobody has within WarnTimeout,
// so an unattended run does not stop on a warning. It never answers anything else, and never while
// the human is typing.
func TestTheContextQuestionTimesOut(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"})
	})
	r.view.Draft = true // a draft in the input box does not matter: keys go to the dialog
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("answered early: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "3" {
		t.Fatalf("typed %q", r.in.text)
	}
	if d, _ := r.state().Run.Dialogs.Only(); d.AutoAnswered.IsZero() {
		t.Fatal("the answer is not marked as baton's own")
	}
	r.run(5 * time.Minute)
	if len(r.in.text) != 1 {
		t.Fatalf("answered twice: %q", r.in.text)
	}
	if b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl")); !strings.Contains(string(b), `"kind":"auto_answered"`) ||
		!strings.Contains(string(b), `"key":"3"`) || !strings.Contains(string(b), `"dialog":"context_warning"`) {
		t.Fatalf("events: %s", b)
	}

	// Any other question waits for the human, however long.
	r = newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now})
	})
	r.run(DefaultTiming.WarnTimeout * 3)
	if len(r.in.text) != 0 {
		t.Fatalf("answered a question that was not baton's: %q", r.in.text)
	}

	// Where baton's mod runs in the lead's claude, the mod settles it, and nothing types into a dialog.
	r = newIdleRig(t)
	helloFrom(t, r.loop.Store)
	r.set(func(st *state.State) {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"})
	})
	r.run(DefaultTiming.WarnTimeout * 3)
	if len(r.in.text) != 0 {
		t.Fatalf("typed %q with baton's mod in the lead's claude", r.in.text)
	}
}

// Every key the human presses starts the clock over: somebody at the keyboard is answering.
func TestAKeyRestartsTheClock(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"})
	})
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	r.view.LastHumanKey = r.now // an arrow key in the dialog
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("answered while the human was at it: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "3" {
		t.Fatalf("typed %q", r.in.text)
	}
}

// baton types only when its question is the only dialog open. Another dialog may be on screen in front
// of it (spikes/16-escalation, S1-D), and there a key would answer that dialog instead. The clock starts
// when the question is first alone.
func TestOnlyAnsweredWhenAlone(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "sub", Key: "k", Since: r.now})
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now.Add(time.Second), Kind: "context_warning"})
	})
	r.run(DefaultTiming.WarnTimeout * 2)
	if len(r.in.text) != 0 {
		t.Fatalf("typed with another dialog open: %q", r.in.text)
	}
	r.set(func(st *state.State) { st.Run.Dialogs.CloseAgent("sub") })
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("the clock ran while the question was not alone: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "3" {
		t.Fatalf("typed %q", r.in.text)
	}

	// A dialog that joins behind it stops baton too: the question might already be answered and gone.
	r = newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"})
	})
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "sub", Key: "k", Since: r.now})
	})
	r.run(DefaultTiming.WarnTimeout)
	if len(r.in.text) != 0 {
		t.Fatalf("typed with a second dialog open: %q", r.in.text)
	}
}

// Asleep, nobody can answer: the clock starts over at the wake.
func TestSleepRestartsTheQuestionClock(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Since: r.now, Kind: "context_warning"})
	})
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	r.tick(time.Hour)
	r.run(DefaultTiming.WarnTimeout - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("answered right after waking: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 {
		t.Fatalf("typed %q", r.in.text)
	}
}
