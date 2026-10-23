package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// weekly records the lead's status line reading of the weekly window, now: used percent, resetting at
// resets.
func (x *crew) weekly(used float64, resets time.Time) {
	x.t.Helper()
	if _, err := x.lead.Update(func(st *state.State) error {
		state.SawUsage(st, state.UsageReading{HasWeekly: true, WeeklyPct: used, WeeklyResets: resets}, x.now)
		return nil
	}); err != nil {
		x.t.Fatal(err)
	}
}

// leadDone reports the lead's phase done, as its `baton done` does, and returns the phase it takes up next.
func (x *crew) leadDone(phase string) string {
	x.t.Helper()
	var next string
	if _, err := x.lead.Update(func(st *state.State) error {
		var err error
		next, err = state.Done(st, x.pl, phase, x.now, state.DoneOpts{})
		return err
	}); err != nil {
		x.t.Fatal(err)
	}
	return next
}

// pushed reports a notice of kind pushed to the human, or still to push in st.
func pushed(x *crew, st state.State, kind string) bool {
	return hasNotice(st, kind) || x.logged("notified", map[string]any{"notice": kind})
}

// woundDown is a crew of no phase agents whose run winds down at the reserve's line, the lead's P0 done, so
// that nothing is under way and every phase left waits for the weekly reset at resets.
func woundDown(t *testing.T, resets time.Time) *crew {
	x := newCrew(t, 2)
	x.c.Fleet, x.c.WeeklyReserve, x.leadIdle = nil, 2, true
	x.tick(time.Second)
	x.weekly(99.1, resets)
	x.tick(time.Second)
	if !x.state().WoundDown() {
		t.Fatalf("not wound down: %+v", x.state().Usage)
	}
	if next := x.leadDone("P0"); next != "" {
		t.Fatalf("the lead took %s while wound down", next)
	}
	return x
}

// At the line, read as a decimal, and not below it, the run winds down once: announced with what finishes,
// pushed and logged. No new agent starts, held by the wind-down even with every slot at work; the agent
// at work goes on, and the lead takes up no new phase.
func TestTheWeeklyWindDownStartsNothingNew(t *testing.T) {
	x := newCrew(t, 2)
	x.c.WeeklyReserve = 2
	x.atWork("P1")
	resets := x.now.Add(30 * time.Hour)
	x.weekly(97.9, resets)
	x.tick(time.Second)
	if x.state().WoundDown() || x.logged("weekly_wind_down", nil) {
		t.Fatalf("wound down below the line: %+v", x.state().Usage)
	}
	x.weekly(98.3, resets)
	x.tick(time.Second)
	x.tick(time.Second)
	st := x.state()
	if !st.WoundDown() || !st.Usage.WindFor.Equal(resets) {
		t.Fatalf("usage %+v", st.Usage)
	}
	if n := x.count("weekly_wind_down", map[string]any{"weekly_pct": 98.3, "reserve": 2, "finishing": []string{"P0", "P1"}, "paused": false}); n != 1 {
		t.Errorf("%d weekly_wind_down:\n%s", n, x.events())
	}
	ann := strings.Join(st.Run.Announce, "\n")
	if !strings.Contains(ann, "baton: 1.7% of weekly usage left: nothing new starts. P0 and P1 finish. baton goes on at") || !pushed(x, st, "weekly_wind_down") {
		t.Errorf("announced %q, events:\n%s", ann, x.events())
	}
	x.tick(AgentRamp) // past the ramp, the second slot free
	x.c.Fleet.Max = 1 // and every slot at work: the wind-down is named, not the slots
	x.tick(time.Second)
	st = x.state()
	if h := st.Hold; h == nil || h.Kind != state.HoldWeekly || h.Phase != "P2" {
		t.Fatalf("hold %+v", st.Hold)
	}
	if got := x.spawns(); len(got) != 1 || x.status("P1") != state.WorkerWorking {
		t.Fatalf("spawns %q, P1 %s", got, x.status("P1"))
	}
	if strings.Contains(strings.Join(st.Run.Announce, "\n"), "no worker on P2 for now: nothing new starts") || x.count("worker_deferred", map[string]any{"held": state.HoldWeekly}) != 1 {
		t.Errorf("the hold was told as a worker's:\n%s", x.events())
	}
	if next := x.leadDone("P0"); next != "" {
		t.Fatalf("the lead took %s", next)
	}
	x.tick(time.Second)
	if st := x.state(); st.Current != "" || x.count("weekly_wind_down", nil) != 1 {
		t.Fatalf("lead %q; wound down %d times", st.Current, x.count("weekly_wind_down", nil))
	}
}

// The run goes on at the weekly reset it stored plus two minutes, never before, with no fresh reading: the
// lead takes up its next phase after a compaction, announced, pushed and logged. Waiting with nothing under
// way is no stall.
func TestTheWindDownGoesOnAtTheStoredReset(t *testing.T) {
	resets := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	x := woundDown(t, resets)
	x.wait(40 * time.Minute)
	if n := x.count("run_stalled", nil); n != 0 {
		t.Fatalf("the wind-down's wait was a stall:\n%s", x.events())
	}
	resume := resets.Add(state.ResumeGrace)
	for x.now.Add(time.Hour).Before(resume) {
		x.tick(time.Hour)
	}
	x.tick(resume.Sub(x.now) - time.Second)
	if st := x.state(); !st.WoundDown() || st.Current != "" {
		t.Fatalf("went on early: %+v, lead %q", st.Usage, st.Current)
	}
	x.tick(time.Second)
	st := x.state()
	if st.WoundDown() || st.Current != "P1" || !st.BoundaryOwed || !st.Usage.WoundUp.Equal(x.now) {
		t.Fatalf("usage %+v, lead %q, boundary %v", st.Usage, st.Current, st.BoundaryOwed)
	}
	if x.count("weekly_wound_up", map[string]any{"by": state.WindReset, "took": "P1", "late": "0s"}) != 1 ||
		x.count("compact_queued", map[string]any{"by": "weekly_wound_up", "next": "P1"}) != 1 {
		t.Errorf("events:\n%s", x.events())
	}
	if ann := strings.Join(st.Run.Announce, "\n"); !strings.Contains(ann, "the weekly usage has reset, so the run goes on, with P1.") || !pushed(x, st, "weekly_wound_up") {
		t.Errorf("announced %q", ann)
	}
}

// A fresh reading below the line before the reset (the human switched accounts) ends the wind-down at once.
func TestAReadingBelowTheLineEndsTheWindDown(t *testing.T) {
	x := woundDown(t, time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	x.tick(time.Hour)
	x.weekly(4.5, x.now.Add(6*24*time.Hour))
	x.tick(time.Second)
	if st := x.state(); st.WoundDown() || st.Current != "P1" || x.count("weekly_wound_up", map[string]any{"by": state.WindReading, "took": "P1"}) != 1 {
		t.Fatalf("usage %+v, lead %q:\n%s", st.Usage, st.Current, x.events())
	}
	x.tick(time.Second)
	if x.count("weekly_wind_down", nil) != 1 {
		t.Errorf("wound down again:\n%s", x.events())
	}
}

// A pause stops the run going on by itself, told once: at the reset nothing goes on. /baton run gives it
// back: past the reset it goes on at once; before it, it goes on at the reset, told so.
func TestAPauseCancelsTheWindDownsGoingOn(t *testing.T) {
	for _, c := range []struct {
		name   string
		before bool // given back before the reset
	}{{"given back after the reset", false}, {"given back before it", true}} {
		t.Run(c.name, func(t *testing.T) {
			resets := time.Date(2026, 10, 11, 18, 0, 0, 0, time.UTC)
			x := woundDown(t, resets)
			x.lead.Update(func(st *state.State) error { return state.Pause(st) })
			x.tick(time.Second)
			x.tick(time.Second)
			st := x.state()
			if x.count("usage_wait_cancelled", map[string]any{"by": state.CancelPause, "wind_down": true}) != 1 ||
				!strings.Contains(strings.Join(st.Run.Announce, "\n"), "going on after the weekly reset cancelled (you paused baton)") {
				t.Fatalf("not told:\n%s", x.events())
			}
			if !c.before {
				x.tick(resets.Add(time.Hour).Sub(x.now))
				if st := x.state(); !st.WoundDown() || x.logged("weekly_wound_up", nil) {
					t.Fatalf("went on while paused: %+v", st.Usage)
				}
			}
			x.lead.Update(func(st *state.State) error { return state.Resume(st) })
			x.tick(time.Second)
			if !c.before {
				if st := x.state(); st.WoundDown() || st.Current != "P1" || x.count("weekly_wound_up", map[string]any{"by": state.WindReset}) != 1 {
					t.Fatalf("given back: %+v, lead %q:\n%s", st.Usage, st.Current, x.events())
				}
				return
			}
			st = x.state()
			if !st.WoundDown() || !st.Usage.WindPaused.IsZero() || x.count("usage_wait", map[string]any{"by": "wind_down", "rearmed": true}) != 1 ||
				!strings.Contains(strings.Join(st.Run.Announce, "\n"), "the run still winds down") {
				t.Fatalf("given back: %+v:\n%s", st.Usage, x.events())
			}
			x.tick(resets.Add(state.ResumeGrace).Sub(x.now))
			if st := x.state(); st.WoundDown() || st.Current != "P1" {
				t.Fatalf("at the reset: %+v, lead %q", st.Usage, st.Current)
			}
		})
	}
}
