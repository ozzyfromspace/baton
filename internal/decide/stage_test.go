package decide

import (
	"errors"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

func TestStageLines(t *testing.T) {
	at := time.Date(2026, 10, 17, 14, 2, 11, 0, time.UTC)
	cases := []struct{ got, want string }{
		{StageStart("unit", at, nil, ""), "▶ unit 14:02:11"},
		{StageStart("unit", at, []string{"TestA", "TestB"}, "j80"), "▶ unit 14:02:11 — only the 2 that failed in j80: TestA, TestB"},
		{StageStart("unit", at, []string{"a", "b", "c", "d", "e", "f"}, ""), "▶ unit 14:02:11 — only the 6 that failed in the gate this reruns: a, b, c, d, e and 1 more"},
		{StageEnd("unit", 0, 2*time.Minute+14*time.Second), "■ unit passed in 2m14s"},
		{StageEnd("e2e", 1, 12*time.Second), "■ e2e failed (exit 1) in 12s"},
		{StageCarried("lint", "j80", "72b7740aaaaa"), "■ lint passed in j80 on 72b7740, so this rerun does not run it again"},
		{StageCarried("lint", "", ""), "■ lint passed in the gate this reruns, so this rerun does not run it again"},
		{StageHalted("e2e", lane.Stage{Name: "unit", Status: lane.StageFailed, Exit: 2}),
			"■ e2e skipped: stage unit failed (exit 2) earlier in this gate, and a gate stops at its first failure " +
				"(a stage run with baton stage --keep-going lets the stages after it run)"},
		{StageNoRun("unit", errors.New("not found")), "baton: stage unit could not run its command: not found"},
		{Stopwatch(0), "0s"},
		{Stopwatch(59*time.Second + 600*time.Millisecond), "1m00s"},
		{Stopwatch(2*time.Minute + 14*time.Second), "2m14s"},
		{Stopwatch(time.Hour + 2*time.Minute + 30*time.Second), "1h02m"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}
