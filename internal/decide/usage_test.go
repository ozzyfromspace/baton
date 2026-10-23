package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestClock(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 6, 0, 0, time.Local) // a Wednesday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(90 * time.Minute), "04:36"},
		{time.Date(2026, 10, 22, 0, 30, 0, 0, time.Local), "Thu 00:30"},
		{time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local), "Mon 10:00"},
		{time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local), "Oct 28 10:00"},
		{now.Add(-25 * time.Hour), "Oct 20 02:06"},
	} {
		if got := Clock(c.t, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.t, got, c.want)
		}
	}
}

// A limit's announcement names its window and reset, never claims a session resumes by itself, and says
// when baton tries again if the reset is unknown.
func TestUsageLimited(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 6, 0, 0, time.Local)
	week := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name, who, window string
		resets, until     time.Time
		want              []string
	}{
		{"weekly", "the worker on P3", state.WindowWeekly, week, week.Add(time.Minute),
			[]string{"the worker on P3 hit the account's weekly usage limit, which resets at Mon 10:00", "until Mon 10:01, then resumes them"}},
		{"five-hour", "this session", state.WindowFiveHour, now.Add(time.Hour), now.Add(time.Hour + time.Minute),
			[]string{"this session hit the account's five-hour usage limit, which resets at 04:06", "until 04:07"}},
		{"another kind", "this session", "overage", now.Add(time.Hour), now.Add(time.Hour + time.Minute), []string{"usage limit (overage)"}},
		{"unknown", "this session", state.WindowUnknown, time.Time{}, now.Add(15 * time.Minute),
			[]string{"hit the account's usage limit. No worker starts", "until 03:21: when it resets is unknown, so baton tries again then"}},
	} {
		got := UsageLimited(c.who, c.window, c.resets, c.until, now)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
		if strings.Contains(got, "by itself") || strings.Contains(got, "resumes each session itself") {
			t.Errorf("%s: %q", c.name, got)
		}
	}
	if got := UsageLifted("this session"); !strings.Contains(got, "this session got a reply") {
		t.Errorf("%q", got)
	}
}
