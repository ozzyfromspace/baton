package decide

import (
	"testing"
	"time"
)

func TestRecovered(t *testing.T) {
	const why = "claude exited with code 137"
	for _, c := range []struct {
		name  string
		w     Words
		phase string
		fresh bool
		want  string
	}{
		{"resumed", Words{Git: true}, "P3", false,
			"[baton] claude exited with code 137 mid-plan, and baton resumed this conversation: the turn that was under way was lost. " +
				"Continue P3: some of it may be done already, so check what is (`git log`, `git status`, and the notes the phase left) before you redo anything, then go on from there."},
		{"resumed, no git", Words{}, "P3", false,
			"[baton] claude exited with code 137 mid-plan, and baton resumed this conversation: the turn that was under way was lost. " +
				"Continue P3: some of it may be done already, so check what is (the files the phase is about, and the notes it left) before you redo anything, then go on from there."},
		{"fresh", Words{Git: true}, "P3", true,
			"[baton] claude exited with code 137 mid-plan and its conversation could not be resumed, so baton started this fresh session: the brief above has P3's instructions. Continue P3 from there."},
		{"fresh, no phase in hand", Words{Git: true}, "", true,
			"[baton] claude exited with code 137 mid-plan and its conversation could not be resumed, so baton started this fresh session: the context above says where the plan stands. Go on from there."},
	} {
		if got := c.w.Recovered(c.phase, why, c.fresh); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestStoppedRecovering(t *testing.T) {
	if got, want := RecoveryCapped(3, 30*time.Minute, "sess-1"),
		"claude exited unexpectedly 3 times in 30 minutes, so baton stopped resuming it. To go on, run `baton --resume sess-1`."; got != want {
		t.Errorf("capped:\n got %q\nwant %q", got, want)
	}
	if got, want := FreshSpent("", "claude exited with code 1", "sess-1"),
		"claude exited with code 1 before its session started, and baton had started a fresh session for the plan already, so it stopped trying. "+
			"Once claude starts again, run `baton --resume sess-1`."; got != want {
		t.Errorf("fresh spent:\n got %q\nwant %q", got, want)
	}
}
