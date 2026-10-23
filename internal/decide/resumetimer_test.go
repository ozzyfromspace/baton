package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The timer's words: when the run resumes, unless the human pauses or writes, and what cancelled it.
func TestTheResumeTimersWords(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 30, 0, 0, time.Local) // a Friday
	monday := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name, got string
		want      []string
	}{
		{"armed for a weekly reset", UsageWait("the worker on P3", state.WindowWeekly, monday, monday.Add(2*time.Minute), now, 0),
			[]string{"baton: the worker on P3 hit the account's weekly usage limit, until Mon 10:00.", "baton resumes the run at 10:02 unless you pause or write."}},
		{"armed for a five-hour reset", UsageWait("this session", state.WindowFiveHour, now.Add(time.Hour), now.Add(time.Hour+2*time.Minute), now, 0),
			[]string{"five-hour usage limit, until 10:30.", "resumes the run at 10:32 unless"}},
		{"backing off", UsageWait("this session", state.WindowUnknown, time.Time{}, now.Add(5*time.Minute), now, 2),
			[]string{"met the account's usage limit again just after it reset (try 2). baton tries again at 09:35 unless you pause or write."}},
		{"a session's retry", UsageReset("P7"), []string{"[baton] The usage limit has reset. Continue P7."}},
		{"the lead's, between phases", UsageReset(""), []string{"[baton] The usage limit has reset. Carry on."}},
		{"fired", UsageResumed([]string{"this session", "the worker on P3"}), []string{"baton resumed this session, the worker on P3."}},
		{"fired, none stopped", UsageResumed(nil), []string{"the run goes on"}},
		{"cancelled by a pause", ResumeCancelled(state.CancelPause, false), []string{"(you paused baton): baton will not resume by itself; /baton run resumes."}},
		{"cancelled by a prompt", ResumeCancelled(state.CancelPrompt, false), []string{"you wrote while it waited, so baton paused the run"}},
		{"the human's own turn hit it", ResumeCancelled(state.CancelPrompt, true), []string{"the turn that hit it was yours"}},
		{"band, armed", BandLimit(monday.Add(2*time.Minute), monday.Add(2*time.Minute), false, now), []string{"usage limit · resumes at Mon 10:02"}},
		{"band, cancelled", BandLimit(now.Add(time.Hour), time.Time{}, true, now), []string{"usage limit until 10:30 · auto-resume cancelled"}},
		{"band, no timer", BandLimit(now.Add(time.Hour), time.Time{}, false, now), []string{"usage limit until 10:30"}},
		{"board, armed", BoardLimit("P3", monday, monday, false, now), []string{"the agent on P3 hit it", "until Mon 10:00; baton resumes the run at Mon 10:00 unless you pause or write"}},
		{"board, cancelled", BoardLimit("lead", now.Add(time.Hour), time.Time{}, true, now), []string{"the lead hit it", "until 10:30; auto-resume cancelled: /baton run resumes"}},
		{"status, armed", StatusResume(monday, "", now), []string{" · baton resumes the run at Mon 10:00 unless you pause or write"}},
		{"status, cancelled", StatusResume(time.Time{}, state.CancelPrompt, now), []string{" · auto-resume cancelled (prompt): /baton run resumes"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s: %q lacks %q", c.name, c.got, w)
			}
		}
	}
	if s := StatusResume(time.Time{}, "", now); s != "" {
		t.Errorf("no timer: %q", s)
	}
}
