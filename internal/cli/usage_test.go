package cli

import (
	"fmt"
	"testing"
	"time"
)

// The status line's weekly window is recorded beside the five-hour one, and a reading of one window alone
// keeps what was recorded of the other.
func TestTheStatusLineRecordsTheWeeklyWindow(t *testing.T) {
	_, lead := asWorker(t)
	lead.env["HOME"] = t.TempDir()
	five, week := lead.now.Add(50*time.Minute).Truncate(time.Second), lead.now.Add(6*24*time.Hour).Truncate(time.Second)
	lead.must(fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":19,"resets_at":%d},"seven_day":{"used_percentage":38.5,"resets_at":%d}}}`,
		lead.env["CLAUDE_CODE_SESSION_ID"], five.Unix(), week.Unix()), "statusline")
	u := lead.state().Usage
	if u == nil || u.UsedPct != 19 || !u.Resets.Equal(five) || u.WeeklyPct != 38.5 || !u.WeeklyResets.Equal(week) || !u.At.Equal(lead.now) {
		t.Fatalf("usage %+v", u)
	}

	// The five-hour window reset, and Claude Code left it out: the weekly reading is still news.
	first := lead.now
	lead.now = lead.now.Add(time.Minute)
	lead.must(fmt.Sprintf(`{"session_id":%q,"rate_limits":{"seven_day":{"used_percentage":39,"resets_at":%d}}}`,
		lead.env["CLAUDE_CODE_SESSION_ID"], week.Unix()), "statusline")
	if u := lead.state().Usage; u.UsedPct != 19 || u.WeeklyPct != 39 || !u.At.Equal(lead.now) {
		t.Errorf("usage %+v", u)
	}

	// The same weekly reading a minute on is not written again.
	lead.now = lead.now.Add(time.Minute)
	lead.must(fmt.Sprintf(`{"session_id":%q,"rate_limits":{"seven_day":{"used_percentage":39,"resets_at":%d}}}`,
		lead.env["CLAUDE_CODE_SESSION_ID"], week.Unix()), "statusline")
	if u := lead.state().Usage; !u.At.Equal(first.Add(time.Minute)) {
		t.Errorf("the same reading was written again: %+v", u)
	}
}
