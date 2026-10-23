package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// limitAt ends the last turn of the session at (the lead's run, or an agent's sub-run) on the account's
// usage limit, now: of window, which resets at resets ("" and zero: the stop said neither).
func (x *crew) limitAt(at *state.Store, window string, resets time.Time) {
	x.t.Helper()
	now := x.now
	if _, err := at.Update(func(st *state.State) error {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastStop = false, now.Add(-time.Minute), now
		st.Run.LastError = &state.StopError{Error: "rate_limit", Window: window, Resets: resets, At: now}
		return nil
	}); err != nil {
		x.t.Fatal(err)
	}
}

// retries counts the retries the lead's loop typed after an API error, and those the resume timer let go.
func (x *crew) retries() (all, timer int) {
	return x.count("error_retry", nil), x.count("error_retry", map[string]any{"by": "resume_timer"})
}

// resets counts the messages the mod sent an agent that its usage limit has reset.
func (x *crew) resets() int {
	n := 0
	for _, m := range x.sent {
		if strings.Contains(m.text, "The usage limit has reset") || strings.Contains(m.text, "API error (rate_limit)") {
			n++
		}
	}
	return n
}

// The timer arms only for a run working on its own, with the limit's window known: at the reset the stop
// named plus two minutes, announced, pushed and logged. The human's own turn that hit it, and a paused
// run, leave it cancelled until /baton run; a limit whose reset is unknown is the 15-minute retry's.
func TestTheResumeTimerArmsForARunOnItsOwn(t *testing.T) {
	for _, c := range []struct {
		name   string
		by     string // "lead", or a worker's phase
		turnBy string // who started the lead's last turn
		paused bool
		window string
		armed  bool
		cancel string // who cancelled it ("" none)
	}{
		{"baton's turn at the lead", "lead", "baton", false, state.WindowWeekly, true, ""},
		{"an agent's, the human at the lead", "P1", "human", false, state.WindowFiveHour, true, ""},
		{"the human's own turn", "lead", "human", false, state.WindowWeekly, false, state.CancelPrompt},
		{"a paused run", "lead", "baton", true, state.WindowWeekly, false, state.CancelPause},
		{"the reset unknown", "lead", "baton", false, "", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 2)
			x.tick(time.Second)
			x.lead.Update(func(st *state.State) error {
				st.Run.TurnBy = c.turnBy
				if c.paused {
					return state.Pause(st)
				}
				return nil
			})
			at := x.lead
			if c.by != "lead" {
				at = x.sub(c.by)
			}
			hit, resets := x.now, time.Time{}
			if c.window != "" {
				resets = x.now.Add(3 * 24 * time.Hour)
			}
			x.limitAt(at, c.window, resets)
			x.tick(time.Second)
			st := x.state()
			u := st.Usage
			if u == nil || !u.ResumeAt.IsZero() != c.armed || u.Cancelled != c.cancel {
				t.Fatalf("usage %+v", u)
			}
			if c.armed && !u.ResumeAt.Equal(resets.Add(state.ResumeGrace)) {
				t.Errorf("armed for %s", u.ResumeAt)
			}
			if c.window == "" && !u.Limited.Equal(hit.Add(DefaultTiming.RateLimitRetry)) {
				t.Errorf("held until %s", u.Limited)
			}
			if paused := st.Mode == state.ModePaused; paused != (c.cancel != "") {
				t.Errorf("mode %s", st.Mode)
			}
			ann := strings.Join(st.Run.Announce, "\n")
			if n := x.count("usage_wait", map[string]any{"by": c.by, "window": c.window}); (n == 1) != c.armed || c.armed != strings.Contains(ann, "unless you pause or write") {
				t.Errorf("%d usage_wait; announced %q", n, ann)
			}
			if c.armed && !strings.Contains(x.events(), `"notice":"usage_wait"`) && !hasNotice(st, "usage_wait") {
				t.Errorf("not pushed:\n%s", x.events())
			}
			if n := x.count("usage_wait_cancelled", map[string]any{"by": c.cancel, "at_hit": true}); (n == 1) != (c.cancel != "") ||
				(c.cancel != "") != strings.Contains(ann, "baton will not resume by itself; /baton run resumes") {
				t.Errorf("%d usage_wait_cancelled; announced %q", n, ann)
			}
			x.tick(time.Second)
			if n := x.count("usage_wait_cancelled", nil); n > 1 {
				t.Errorf("the cancel was told %d times", n)
			}
		})
	}
}

func hasNotice(st state.State, kind string) bool {
	for _, n := range st.Run.Notices {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// While the timer is armed nothing is retried, however far off a weekly reset is; as it fires, the lead
// and the agent stopped mid-phase each retry once, and never again for that firing.
func TestTheResumeTimerWakesEachStoppedSessionOnce(t *testing.T) {
	x := newCrew(t, 2)
	x.atWork("P1")
	x.leadIdle = true
	resets := x.now.Add(3*24*time.Hour + 5*time.Hour)
	x.limitAt(x.lead, state.WindowWeekly, resets)
	x.limitAt(x.sub("P1"), state.WindowWeekly, resets)
	x.tick(time.Second)
	resume := resets.Add(state.ResumeGrace)
	if u := x.state().Usage; u == nil || !u.ResumeAt.Equal(resume) {
		t.Fatalf("usage %+v", u)
	}
	for x.now.Add(time.Hour).Before(resume) {
		x.tick(time.Hour)
	}
	if all, _ := x.retries(); all != 0 || x.resets() != 0 {
		t.Fatalf("retried while armed: lead %d, agent %d", all, x.resets())
	}
	x.tick(resume.Sub(x.now) + time.Second)
	x.tick(time.Second)
	x.tick(time.Second)
	if all, timer := x.retries(); all != 1 || timer != 1 || x.resets() != 1 {
		t.Fatalf("as it fired: lead %d (%d the timer's), agent %d; sent %+v", all, timer, x.resets(), x.sent)
	}
	if !strings.Contains(x.sent[len(x.sent)-1].text, "[baton] The usage limit has reset. Continue P1.") {
		t.Errorf("the agent was sent %q", x.sent[len(x.sent)-1].text)
	}
	if n := x.count("usage_resumed", nil); n != 1 || !strings.Contains(x.events(), `"sessions":["lead","P1"]`) {
		t.Errorf("%d usage_resumed:\n%s", n, x.events())
	}
	if !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), "baton resumed this session, the worker on P1") {
		t.Errorf("announced %q", x.state().Run.Announce)
	}
	for range 10 {
		x.tick(time.Minute)
	}
	if all, _ := x.retries(); all != 1 || x.resets() != 1 || x.count("usage_resumed", nil) != 1 {
		t.Errorf("woken again: lead %d, agent %d", all, x.resets())
	}
}

// A limit whose reset is unknown arms nothing: the retry goes RateLimitRetry after the hit, as before.
func TestAnUnknownResetKeepsTheRetry(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet = nil
	x.leadIdle = true
	x.tick(time.Second)
	hit := x.now
	x.limitAt(x.lead, "", time.Time{})
	for x.now.Before(hit.Add(DefaultTiming.RateLimitRetry - time.Minute)) {
		x.tick(time.Minute)
	}
	if all, _ := x.retries(); all != 0 || x.count("usage_wait", nil) != 0 {
		t.Fatalf("retried early, or armed:\n%s", x.events())
	}
	x.tick(2 * time.Minute)
	if all, timer := x.retries(); all != 1 || timer != 0 {
		t.Errorf("retries %d (%d the timer's):\n%s", all, timer, x.events())
	}
}

// Hit again just after the timer fired, the next try backs off 2, 5, 10, then every 15 minutes, logged
// with its try; a stop that brings a reset of its own arms at that reset, afresh.
func TestTheResumeTimerBacksOff(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet = nil
	x.leadIdle = true
	x.tick(time.Second)
	x.limitAt(x.lead, state.WindowFiveHour, x.now.Add(time.Hour))
	x.tick(time.Second)
	for i, want := range []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		u := x.state().Usage
		x.tick(u.ResumeAt.Sub(x.now) + time.Second)
		x.tick(time.Second)
		if !x.state().Usage.ResumeAt.IsZero() {
			t.Fatalf("try %d did not fire: %+v", i, x.state().Usage)
		}
		x.tick(30 * time.Second)
		hit := x.now
		x.limitAt(x.lead, "", time.Time{})
		x.tick(time.Second)
		if u := x.state().Usage; !u.ResumeAt.Equal(hit.Add(want)) || u.Tries != i+1 {
			t.Fatalf("try %d: armed %s, tries %d, want %s", i+1, u.ResumeAt.Sub(hit), u.Tries, want)
		}
		if !x.logged("usage_wait", map[string]any{"tries": i + 1}) {
			t.Errorf("try %d not logged", i+1)
		}
	}
	if all, timer := x.retries(); all != 5 || timer != 5 {
		t.Errorf("retries %d (%d the timer's)", all, timer)
	}
	x.tick(time.Minute)
	resets := x.now.Add(3 * time.Hour)
	x.limitAt(x.lead, state.WindowFiveHour, resets)
	x.tick(time.Second)
	if u := x.state().Usage; !u.ResumeAt.Equal(resets.Add(state.ResumeGrace)) || u.Tries != 0 {
		t.Errorf("a reset of its own: %+v", u)
	}
}

// A pause cancels the timer, told once: nothing is retried past the reset, the agent's retry included,
// until /baton run, which arms it again, here at once, its time gone by.
func TestAPauseCancelsTheResumeTimer(t *testing.T) {
	x := newCrew(t, 2)
	x.atWork("P1")
	x.leadIdle = true
	resets := x.now.Add(2 * time.Hour)
	x.limitAt(x.lead, state.WindowFiveHour, resets)
	x.limitAt(x.sub("P1"), state.WindowFiveHour, resets)
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error { return state.Pause(st) })
	x.tick(time.Second)
	x.tick(time.Second)
	if u := x.state().Usage; !u.ResumeAt.IsZero() || u.Cancelled != state.CancelPause || u.CancelSaid.IsZero() {
		t.Fatalf("usage %+v", u)
	}
	if n := x.count("usage_wait_cancelled", map[string]any{"by": "pause"}); n != 1 {
		t.Errorf("%d usage_wait_cancelled", n)
	}
	if !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), "(you paused baton): baton will not resume by itself") {
		t.Errorf("announced %q", x.state().Run.Announce)
	}
	for x.now.Before(resets.Add(time.Hour)) {
		x.tick(10 * time.Minute)
	}
	if all, _ := x.retries(); all != 0 || x.resets() != 0 || x.count("usage_resumed", nil) != 0 {
		t.Fatalf("retried while paused: lead %d, agent %d", all, x.resets())
	}
	x.lead.Update(func(st *state.State) error { return state.Resume(st) })
	x.tick(time.Second)
	x.tick(time.Second)
	x.tick(time.Second)
	if all, timer := x.retries(); all != 1 || timer != 1 || x.resets() != 1 || x.count("usage_resumed", nil) != 1 {
		t.Errorf("after /baton run: lead %d (%d the timer's), agent %d", all, timer, x.resets())
	}
}

// /baton run before the reset arms the timer again, at the end of the hold, and says so.
func TestBatonRunArmsTheResumeTimerAgain(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet = nil
	x.tick(time.Second)
	resets := x.now.Add(2 * time.Hour)
	x.limitAt(x.lead, state.WindowFiveHour, resets)
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error { return state.Pause(st) })
	x.tick(time.Second)
	x.lead.Update(func(st *state.State) error { return state.Resume(st) })
	x.tick(time.Second)
	if u := x.state().Usage; !u.ResumeAt.Equal(resets.Add(state.ResumeGrace)) || u.Cancelled != "" {
		t.Fatalf("usage %+v", u)
	}
	if !x.logged("usage_wait", map[string]any{"rearmed": true}) {
		t.Errorf("events:\n%s", x.events())
	}
}

// The human's prompt at the lead cancels the timer and pauses the run, told once beside it; so do words
// for a phase's agent, told in the block's reason, as nothing else shows. A /baton command that only looks
// leaves the timer armed.
func TestAPromptCancelsTheResumeTimer(t *testing.T) {
	for _, c := range []struct {
		name, prompt string
		cancels      bool
		in           string // where the human reads it: the hook's systemMessage, or its reason
	}{
		{"a prompt", "how far along is the run?", true, "systemMessage"},
		{"words for a phase's agent", "@P1 look at the flaky test first", true, "reason"},
		{"a look at the run", "/baton status", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 2)
			x.tick(time.Second)
			x.limitAt(x.lead, state.WindowWeekly, x.now.Add(48*time.Hour))
			x.tick(time.Second)
			code, out := x.hook("UserPromptSubmit", map[string]any{"prompt": c.prompt, "transcript_path": x.leadTranscript()})
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			st := x.state()
			if u := st.Usage; (u.Cancelled == state.CancelPrompt) != c.cancels || u.ResumeAt.IsZero() != c.cancels ||
				(st.Mode == state.ModePaused) != c.cancels {
				t.Fatalf("mode %s, usage %+v", st.Mode, u)
			}
			if !c.cancels {
				return
			}
			var res map[string]any
			json.Unmarshal([]byte(out), &res)
			if said, _ := res[c.in].(string); !strings.Contains(said, "baton will not resume by itself; /baton run resumes") {
				t.Errorf("the hook said %s", out)
			}
			if n := x.count("usage_wait_cancelled", map[string]any{"by": "prompt"}); n != 1 {
				t.Errorf("%d usage_wait_cancelled", n)
			}
			x.tick(time.Second)
			if n := x.count("usage_wait_cancelled", nil); n != 1 {
				t.Errorf("told %d times", n)
			}
		})
	}
}

// The timer is kept in the run's state: a new host keeps it, and a laptop asleep through the reset
// resumes the lead as it wakes, not RateLimitRetry after.
func TestTheResumeTimerOutlivesARestartAndASleep(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet = nil
	x.leadIdle = true
	x.tick(time.Second)
	resets := x.now.Add(3 * time.Hour)
	x.limitAt(x.lead, state.WindowFiveHour, resets)
	x.tick(time.Second)
	x.c.Ended("the baton session ended", nil)
	x.proj.Unbind("inst")
	x.c = x.coordinator("next", 2)
	x.c.Fleet = nil
	x.tick(time.Second)
	if u := x.state().Usage; !u.ResumeAt.Equal(resets.Add(state.ResumeGrace)) || !x.c.runLimited().Equal(u.ResumeAt) {
		t.Fatalf("after the restart: %+v, held until %s", u, x.c.runLimited())
	}
	x.tick(5 * time.Hour) // asleep through the reset
	x.tick(time.Second)
	if all, timer := x.retries(); all != 1 || timer != 1 {
		t.Errorf("on waking: retries %d (%d the timer's):\n%s", all, timer, x.events())
	}
}
