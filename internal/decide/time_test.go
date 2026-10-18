package decide

import (
	"strings"
	"testing"
	"time"
)

// TimeFelt says plainly that the model does not feel a gate's time and the human does, with one real
// case, and asks what a check can tell before it runs; about 120 words, so it stays read.
func TestTimeFeltWords(t *testing.T) {
	if !strings.HasPrefix(TimeFelt, "You do not feel time pass. A gate that runs 12 minutes is one tool call to you") {
		t.Errorf("TimeFelt opens with %q", TimeFelt[:60])
	}
	at := 0
	for _, want := range []string{
		"its result arrives as if no time went by.",
		"The human feels every one of those minutes: they wait on the run, their usage and money drain, and the work they care about next sits behind it.",
		"A check that cannot change what ships is not caution: it harms them.",
		"A real run started one 12-minute gate 11 times in 75 minutes, each time after fixing a single failure,",
		"8 hours on three full passes of a 2h40m QA set that mostly re-proved what had passed.",
		"Before any check, ask what it can tell you that you do not already know, and run only that.",
	} {
		i := strings.Index(TimeFelt[at:], want)
		if i < 0 {
			t.Errorf("TimeFelt lacks %q, or has it out of order:\n%s", want, TimeFelt)
			continue
		}
		at += i + len(want)
	}
	if n := len(strings.Fields(TimeFelt)); n < 100 || n > 140 {
		t.Errorf("TimeFelt runs to %d words, want about 120", n)
	}
}

func TestTimeShort(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{12 * time.Minute, "This gate spends 12m of the human's time; to you it is one tool call."},
		{2*time.Hour + 40*time.Minute, "This gate spends 2h40m of the human's time; to you it is one tool call."},
		{45 * time.Second, "This gate spends 45s of the human's time; to you it is one tool call."},
	} {
		if got := TimeShort(c.d); got != c.want {
			t.Errorf("TimeShort(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
