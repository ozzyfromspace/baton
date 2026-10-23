package state

import (
	"slices"
	"testing"
	"time"
)

// The run winds down at the reserve's line or above, read as a decimal, for a window whose reset is ahead,
// and once a window: a window of its own (another account) winds it down again.
func TestWindDown(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	week := at.Add(3 * 24 * time.Hour)
	other := at.Add(5 * 24 * time.Hour)
	for _, c := range []struct {
		name    string
		u       *Usage
		reserve float64
		want    bool
		resets  time.Time
	}{
		{"at the line", &Usage{WeeklyPct: 98, WeeklyResets: week}, 2, true, week},
		{"over it", &Usage{WeeklyPct: 99.6, WeeklyResets: week}, 2, true, week},
		{"just below it", &Usage{WeeklyPct: 97.9, WeeklyResets: week}, 2, false, time.Time{}},
		{"a decimal line", &Usage{WeeklyPct: 98.5, WeeklyResets: week}, 1.5, true, week},
		{"just below a decimal line", &Usage{WeeklyPct: 98.49, WeeklyResets: week}, 1.5, false, time.Time{}},
		{"the reserve off", &Usage{WeeklyPct: 100, WeeklyResets: week}, 0, false, time.Time{}},
		{"no reading", nil, 2, false, time.Time{}},
		{"a reading whose reset passed", &Usage{WeeklyPct: 99, WeeklyResets: at.Add(-time.Minute)}, 2, false, time.Time{}},
		{"wound down for the window already", &Usage{WeeklyPct: 99, WeeklyResets: week.Add(time.Second), WoundDown: at.Add(-time.Hour), WindFor: week}, 2, false, week},
		{"another window", &Usage{WeeklyPct: 99, WeeklyResets: other, WoundDown: at.Add(-time.Hour), WindFor: week}, 2, true, other},
	} {
		st := New()
		st.Mode, st.Usage = ModeRunning, c.u
		if got := WindDown(&st, c.reserve, at); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
		if c.want && (!st.Usage.WoundDown.Equal(at) || !st.WoundDown()) {
			t.Errorf("%s: %+v", c.name, st.Usage)
		}
		if u := st.Usage; u != nil && !u.WindFor.Equal(c.resets) {
			t.Errorf("%s: waits for %s, want %s", c.name, u.WindFor, c.resets)
		}
		if u := st.Usage; c.want && !u.GoesOnAt().Equal(c.resets.Add(ResumeGrace)) {
			t.Errorf("%s: goes on at %s", c.name, u.GoesOnAt())
		}
	}
}

// A wound-down run goes on at its stored reset plus the grace, unless paused; on a reading below the line,
// paused too; and with the reserve off.
func TestWindUpBy(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	week := at.Add(3 * 24 * time.Hour)
	wound := Usage{WeeklyPct: 98.2, WeeklyResets: week, WoundDown: at, WindFor: week}
	for _, c := range []struct {
		name    string
		mode    string
		u       Usage
		reserve float64
		now     time.Time
		want    string
	}{
		{"before the reset", ModeRunning, wound, 2, week.Add(ResumeGrace - time.Second), ""},
		{"at the reset and its grace", ModeRunning, wound, 2, week.Add(ResumeGrace), WindReset},
		{"paused at the reset", ModePaused, wound, 2, week.Add(time.Hour), ""},
		{"a fresh reading below the line", ModeRunning, Usage{WeeklyPct: 12.5, WeeklyResets: at.Add(6 * 24 * time.Hour), WoundDown: at, WindFor: week}, 2, at.Add(time.Hour), WindReading},
		{"a reading below the line while paused", ModePaused, Usage{WeeklyPct: 12.5, WeeklyResets: at.Add(6 * 24 * time.Hour), WoundDown: at, WindFor: week}, 2, at.Add(time.Hour), WindReading},
		{"the window's old reading after its reset", ModeRunning, Usage{WeeklyPct: 98.2, WeeklyResets: week, WoundDown: at, WindFor: week}, 2, week.Add(time.Minute), ""},
		{"the reserve off", ModeRunning, wound, 0, at.Add(time.Hour), WindOff},
		{"not wound down", ModeRunning, Usage{WeeklyPct: 99, WeeklyResets: week}, 2, week.Add(time.Hour), ""},
	} {
		st := New()
		u := c.u
		st.Mode, st.Usage = c.mode, &u
		if got := WindUpBy(st, c.reserve, c.now); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// While wound down the lead takes up no new phase, but a parked phase it can act on again is under way and
// comes back; going on takes up the next phase after a compaction, and nothing while the lead holds one.
func TestNoNewPhaseWhileWoundDown(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	week := at.Add(3 * 24 * time.Hour)
	p := graph("P0:", "P1:P0", "P2:P0", "P3:P1")
	st := runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseWorking}, nil)
	st.Usage = &Usage{WeeklyPct: 98, WeeklyResets: week}
	if Next(st, p) != "P2" || !WindDown(&st, 2, at) {
		t.Fatalf("before: next %q, %+v", Next(st, p), st.Usage)
	}
	if got := Next(st, p); got != "" {
		t.Fatalf("a new phase while wound down: %q", got)
	}
	if got := NextWorker(st, p); got != "P2" {
		t.Fatalf("the phase a new agent is held from: %q", got) // loop/fleet.go holds it (HoldWeekly)
	}
	if got := UnderWay(st, p); !slices.Equal(got, []string{"P1"}) {
		t.Fatalf("under way: %q", got)
	}
	parked := runState(p, map[string]string{"P0": PhaseDone, "P1": PhaseParked}, map[string]Park{"P1": {Reason: ParkRed, Since: at}})
	parked.Usage = st.Usage
	if got := Next(parked, p); got != "P1" {
		t.Fatalf("a parked phase while wound down: %q", got)
	}

	st.Current = "P1" // the lead holds a phase: going on takes up nothing more
	held := st
	held.Usage = &Usage{}
	*held.Usage = *st.Usage
	if got := WindUp(&held, p, week); got != "" || held.WoundDown() || !held.Usage.WoundUp.Equal(week) || held.BoundaryOwed {
		t.Fatalf("going on with a phase in hand: %q %+v", got, held.Usage)
	}
	st.Current = ""
	if got := WindUp(&st, p, week.Add(ResumeGrace)); got != "P2" || st.Current != "P2" || !st.BoundaryOwed || st.WoundDown() || !st.Usage.WindFor.IsZero() {
		t.Fatalf("going on: %q %+v", got, st)
	}
	if WindUp(&st, p, week.Add(time.Hour)) != "" {
		t.Fatal("went on twice")
	}
}

// A pause stops a wound-down run going on by itself, told once; /baton run arms it again, once; leaving
// baton's hands cancels it, told by that command.
func TestTheWindDownsCancel(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	week := at.Add(3 * 24 * time.Hour)
	st := New()
	st.Mode, st.Usage = ModeRunning, &Usage{WeeklyPct: 98, WeeklyResets: week}
	if WindPause(&st, at) || WindRearm(&st) || WindLeft(&st, at) {
		t.Fatal("cancelled with no wind-down")
	}
	WindDown(&st, 2, at)
	if !st.WindArmed() || WindPause(&st, at) || WindRearm(&st) {
		t.Fatalf("running: %+v", st.Usage)
	}
	if err := Pause(&st); err != nil {
		t.Fatal(err)
	}
	if st.WindArmed() || !WindPause(&st, at) || WindPause(&st, at) || WindUpBy(st, 2, week.Add(time.Hour)) != "" {
		t.Fatalf("paused: %+v", st.Usage)
	}
	if err := Resume(&st); err != nil {
		t.Fatal(err)
	}
	if !WindRearm(&st) || WindRearm(&st) || !st.WindArmed() || WindUpBy(st, 2, week.Add(ResumeGrace)) != WindReset {
		t.Fatalf("given back: %+v", st.Usage)
	}
	if !WindLeft(&st, at) || st.Usage.WindPaused.IsZero() {
		t.Fatalf("left: %+v", st.Usage)
	}
}
