package state

import (
	"testing"
	"time"
)

func TestWindowOf(t *testing.T) {
	for kind, want := range map[string]string{
		"five_hour": WindowFiveHour, "seven_day": WindowWeekly, "seven_day_opus": WindowWeekly,
		"seven_day_overage_included": WindowWeekly, "overage": "overage", "": "",
	} {
		if got := WindowOf(kind); got != want {
			t.Errorf("%q: %q, want %q", kind, got, want)
		}
	}
}

// Which window a limit was: the stop's own word, then a status line reading at 99% or more, the weekly
// window first, then unknown.
func TestLimitWindow(t *testing.T) {
	hit := time.Date(2026, 10, 21, 3, 6, 0, 0, time.UTC)
	five, week := hit.Add(90*time.Minute), hit.Add(4*24*time.Hour+8*time.Hour)
	for _, c := range []struct {
		name   string
		e      *StopError
		u      *Usage
		window string
		resets time.Time
	}{
		{"the transcript's five-hour window", &StopError{Window: WindowFiveHour, Resets: five}, nil, WindowFiveHour, five},
		// A real run's weekly limit, hit at 27% of the five-hour window.
		{"the transcript's weekly window over a five-hour reading", &StopError{Window: WindowWeekly, Resets: week},
			&Usage{UsedPct: 27, Resets: five}, WindowWeekly, week},
		{"another kind keeps its reset", &StopError{Window: "overage", Resets: five}, nil, "overage", five},
		{"the window named, its reset from the reading", &StopError{Window: WindowWeekly},
			&Usage{WeeklyPct: 96, WeeklyResets: week}, WindowWeekly, week},
		{"the weekly reading at 99", nil, &Usage{UsedPct: 99, Resets: five, WeeklyPct: 99.4, WeeklyResets: week}, WindowWeekly, week},
		// Four real stops read 99%, not 100.
		{"the five-hour reading at 99", nil, &Usage{UsedPct: 99, Resets: five, WeeklyPct: 41, WeeklyResets: week}, WindowFiveHour, five},
		{"a reading under the line: unknown", nil, &Usage{UsedPct: 27, Resets: five, WeeklyPct: 98.9, WeeklyResets: week}, WindowUnknown, time.Time{}},
		{"a 429 with no window: unknown", &StopError{}, &Usage{UsedPct: 62, Resets: five}, WindowUnknown, time.Time{}},
		{"nothing at all", nil, nil, WindowUnknown, time.Time{}},
		{"a reset gone by", &StopError{Window: WindowFiveHour, Resets: hit.Add(-time.Minute)}, nil, WindowUnknown, time.Time{}},
		{"a reset past belief", &StopError{Window: WindowWeekly, Resets: hit.Add(MaxReset + time.Second)}, nil, WindowUnknown, time.Time{}},
		{"a stale reading's reset", nil, &Usage{UsedPct: 100, Resets: hit}, WindowUnknown, time.Time{}},
	} {
		window, resets := LimitWindow(c.e, c.u, hit)
		if window != c.window || !resets.Equal(c.resets) {
			t.Errorf("%s: %s %s, want %s %s", c.name, window, resets, c.window, c.resets)
		}
	}
}

// A limit hit keeps its window and reset; the same hit again changes nothing.
func TestRecordLimit(t *testing.T) {
	hit := time.Date(2026, 10, 21, 3, 6, 0, 0, time.UTC)
	week := hit.Add(72 * time.Hour)
	st := New()
	st.Usage = &Usage{UsedPct: 27, Lifted: hit.Add(-time.Hour)}
	if !RecordLimit(&st, "P3", hit, week.Add(time.Minute), WindowWeekly, week) {
		t.Fatal("a new hit was not recorded")
	}
	u := *st.Usage
	if u.By != "P3" || !u.Hit.Equal(hit) || u.Window != WindowWeekly || !u.WindowResets.Equal(week) || !u.Limited.Equal(week.Add(time.Minute)) ||
		u.UsedPct != 27 || !u.Lifted.IsZero() {
		t.Fatalf("%+v", u)
	}
	if RecordLimit(&st, "lead", hit, hit.Add(15*time.Minute), WindowUnknown, time.Time{}) || st.Usage.Window != WindowWeekly {
		t.Errorf("the same hit again: %+v", *st.Usage)
	}
}

// A turn started after a hit that ended with the model's reply lifts the hold; nothing else does.
func TestALimitLiftsOnAReplySinceIt(t *testing.T) {
	hit := time.Date(2026, 10, 21, 3, 6, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		r    Runtime
		want bool
	}{
		{"a turn since, ended with a reply", Runtime{TurnStarted: hit.Add(time.Hour), LastStop: hit.Add(time.Hour + time.Minute)}, true},
		{"a reply before the hit", Runtime{TurnStarted: hit.Add(-time.Minute), LastStop: hit.Add(time.Second)}, false},
		{"a turn since, still open", Runtime{TurnStarted: hit.Add(time.Hour), LastStop: hit.Add(-time.Hour)}, false},
		{"a turn since, ended on an error", Runtime{TurnStarted: hit.Add(time.Hour), LastStop: hit.Add(-time.Hour),
			LastError: &StopError{Error: "rate_limit", At: hit.Add(time.Hour + time.Second)}}, false},
		{"an error, then a reply in a later turn", Runtime{TurnStarted: hit.Add(2 * time.Hour), LastStop: hit.Add(2*time.Hour + time.Minute),
			LastError: &StopError{Error: "rate_limit", At: hit.Add(time.Hour)}}, true},
	} {
		if got := c.r.RepliedSince(hit); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}

	now := hit.Add(time.Hour)
	st := New()
	st.Usage = &Usage{Hit: hit, Limited: hit.Add(72 * time.Hour), Window: WindowWeekly}
	if !LiftLimit(&st, now) || !st.Usage.Limited.Equal(now) || !st.Usage.Lifted.Equal(now) || st.Usage.Window != WindowWeekly {
		t.Fatalf("%+v", *st.Usage)
	}
	if LiftLimit(&st, now.Add(time.Minute)) {
		t.Error("a hold lifted twice")
	}
	if none := New(); LiftLimit(&none, now) {
		t.Error("no hold to lift")
	}
}

// A status line reading is recorded window by window: one it says nothing of keeps what was recorded.
func TestSawUsage(t *testing.T) {
	at := time.Date(2026, 10, 23, 3, 0, 0, 0, time.UTC)
	five, week := at.Add(50*time.Minute), at.Add(6*24*time.Hour)
	both := UsageReading{HasFiveHour: true, FiveHourPct: 19, FiveHourResets: five, HasWeekly: true, WeeklyPct: 38.5, WeeklyResets: week}
	st := New()
	if both.Same(st.Usage) || !(UsageReading{}).Same(nil) {
		t.Fatal("Same on no usage")
	}
	SawUsage(&st, both, at)
	if u := *st.Usage; u.UsedPct != 19 || !u.Resets.Equal(five) || u.WeeklyPct != 38.5 || !u.WeeklyResets.Equal(week) || !u.At.Equal(at) {
		t.Fatalf("%+v", u)
	}
	if !both.Same(st.Usage) {
		t.Error("the same reading is new")
	}
	st.Usage.Limited = at.Add(time.Hour)
	weekly := UsageReading{HasWeekly: true, WeeklyPct: 39, WeeklyResets: week} // the five-hour window's reset passed
	if weekly.Same(st.Usage) {
		t.Error("a new weekly reading is the same")
	}
	SawUsage(&st, weekly, at.Add(time.Minute))
	if u := *st.Usage; u.UsedPct != 19 || u.WeeklyPct != 39 || !u.Limited.Equal(at.Add(time.Hour)) || !u.At.Equal(at.Add(time.Minute)) {
		t.Errorf("%+v", u)
	}
	if !(UsageReading{HasFiveHour: true, FiveHourPct: 19, FiveHourResets: five}).Same(st.Usage) {
		t.Error("a five-hour reading alone, unchanged, is new")
	}
}
