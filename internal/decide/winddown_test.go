package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The wind-down's words: how much is left, what finishes, when baton goes on, and what stops it.
func TestTheWindDownsWords(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 30, 0, 0, time.Local) // a Friday
	monday := time.Date(2026, 10, 26, 10, 2, 0, 0, time.Local)
	for _, c := range []struct {
		name, got string
		want      []string
	}{
		{"two phases finish", WindDown(98, []string{"P3", "P5"}, monday, false, now),
			[]string{"baton: 2% of weekly usage left: nothing new starts. P3 and P5 finish. baton goes on at Mon 10:02, after the weekly reset, unless you pause or write."}},
		{"a decimal reading", WindDown(98.64, []string{"P3"}, monday, false, now), []string{"1.4% of weekly usage left", "P3 finishes."}},
		{"nothing under way", WindDown(99, nil, monday, false, now), []string{"left: nothing new starts. baton goes on"}},
		{"over the allowance", WindDown(100.2, nil, monday, false, now), []string{"baton: 0% of weekly usage left"}},
		{"paused", WindDown(98, []string{"P3", "P5", "P7"}, monday, true, now),
			[]string{"P3, P5 and P7 finish. The run is paused: once /baton run gives it back, baton goes on after the weekly reset, at Mon 10:02."}},
		{"held", WindDownHolds(monday, now), []string{"nothing new starts in the weekly wind-down, until Mon 10:02"}},
		{"done", WindDownDone(monday, now), []string{"no new phase starts until the weekly reset, and baton goes on at Mon 10:02. End your turn now"}},
		{"at the reset", WoundUp(state.WindReset, "P7"), []string{"baton: the weekly usage has reset, so the run goes on, with P7."}},
		{"a reading below the line", WoundUp(state.WindReading, ""), []string{"reads below the reserve again, so the run goes on: new phases start again."}},
		{"the reserve off", WoundUp(state.WindOff, ""), []string{"weekly_reserve_pct is off"}},
		{"cancelled by a pause", WindCancelled(state.CancelPause), []string{"(you paused baton): baton will not go on by itself; /baton run resumes."}},
		{"cancelled by a prompt", WindCancelled(state.CancelPrompt), []string{"you wrote while the run wound down, so baton paused the run"}},
		{"given back", WindRearmed(monday, now), []string{"the run still winds down", "goes on at Mon 10:02 unless you pause or write"}},
		{"status", StatusWindDown(98.2, monday, false, now), []string{"weekly wind-down: 1.8% of weekly usage left → nothing new starts; what is under way finishes · baton goes on at Mon 10:02 unless you pause or write"}},
		{"status, paused", StatusWindDown(98.2, monday, true, now), []string{"· paused: /baton run gives it back, and baton goes on after the weekly reset, at Mon 10:02"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s: %q lacks %q", c.name, c.got, w)
			}
		}
	}
}
