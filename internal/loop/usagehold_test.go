package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A run with no workers holds on a usage limit too: the lead's own retry waits for the window's reset
// instead of meeting the limit again 15 and 30 minutes on, as a real run's lead did, four days before its
// weekly reset.
func TestAUsageLimitHoldsARunWithNoWorkers(t *testing.T) {
	x := newCrew(t, 2)
	x.c.Fleet = nil
	x.leadIdle = true
	x.tick(time.Second)
	hit := x.now
	resets := hit.Add(3 * time.Hour)
	x.lead.Update(func(st *state.State) error {
		st.Run.LastError = &state.StopError{Error: "rate_limit", Window: state.WindowWeekly, Resets: resets, At: hit}
		return nil
	})
	x.tick(time.Second)
	until := resets.Add(limitGrace)
	if u := x.state().Usage; u == nil || !u.Limited.Equal(until) || u.Window != state.WindowWeekly || !u.WindowResets.Equal(resets) {
		t.Fatalf("usage %+v", u)
	}
	if ev := x.events(); strings.Count(ev, `"kind":"usage_limited"`) != 1 {
		t.Fatalf("events:\n%s", ev)
	}
	for x.now.Add(time.Minute).Before(until) {
		x.tick(time.Minute) // under Timing.ClockJump: a longer tick is a sleep, which restarts the retry's clock
	}
	if ev := x.events(); strings.Contains(ev, `"kind":"error_retry"`) {
		t.Fatalf("the lead was retried during the limit:\n%s", ev)
	}
	x.tick(until.Sub(x.now) + time.Second) // the resume timer fires after the lead's tick
	x.tick(time.Second)
	if ev := x.events(); strings.Count(ev, `"kind":"error_retry"`) != 1 || !strings.Contains(ev, `"by":"resume_timer"`) {
		t.Errorf("the lead was not retried once the limit reset:\n%s", ev)
	}
}

// A session of the run that got a reply since the limit was hit lifts the hold, whenever its reset was to
// be (the human switched accounts, say): the hold is logged and announced as lifted, once.
func TestALimitLiftsWhenASessionGetsAReply(t *testing.T) {
	x := newCrew(t, 2)
	x.tick(time.Second)
	hit := x.now
	resets := hit.Add(4 * 24 * time.Hour)
	x.lead.Update(func(st *state.State) error {
		st.Run.LastError = &state.StopError{Error: "rate_limit", Window: state.WindowWeekly, Resets: resets, At: hit}
		return nil
	})
	x.tick(time.Second)
	if got := x.c.runLimited(); !got.Equal(resets.Add(limitGrace)) {
		t.Fatalf("held until %s", got)
	}

	x.tick(10 * time.Minute) // a turn that started before a reply is no proof
	x.lead.Update(func(st *state.State) error { st.Run.TurnStarted, st.Run.TurnOpen = x.now, true; return nil })
	x.tick(time.Second)
	if u := x.state().Usage; !u.Lifted.IsZero() {
		t.Fatalf("lifted by a turn still open: %+v", u)
	}
	x.lead.Update(func(st *state.State) error { st.Run.LastStop, st.Run.TurnOpen = x.now, false; return nil })
	x.tick(time.Second)
	u := x.state().Usage
	if u.Lifted.IsZero() || !u.Limited.Equal(u.Lifted) || x.now.Before(u.Limited) || !x.c.runLimited().Equal(u.Limited) {
		t.Fatalf("not lifted: %+v", u)
	}
	x.tick(time.Second)
	if ev := x.events(); strings.Count(ev, `"kind":"usage_lifted"`) != 1 || !strings.Contains(ev, `"by":"lead"`) {
		t.Errorf("events:\n%s", ev)
	}
	if ann := strings.Join(x.state().Run.Announce, "\n"); !strings.Contains(ann, "this session got a reply from the model since the usage limit was hit") {
		t.Errorf("announced %q", ann)
	}
}
