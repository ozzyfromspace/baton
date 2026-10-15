package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
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

// Past the cap baton waits, and says when it brings claude back, and how the human may first; stopped by
// them, it says how to go on; a refused conversation it resumed is left to them.
func TestRecoveringPastTheCap(t *testing.T) {
	at := time.Date(2026, 10, 15, 15, 4, 0, 0, time.Local)
	for _, c := range []struct{ name, got, want string }{
		{"the backoff", RecoveryBackoff(3, 30*time.Minute, "claude exited with code 137", at, 15*time.Minute, "sess-1"),
			"claude exited unexpectedly 3 times in 30 minutes (the last: claude exited with code 137), so baton waits before it brings it back: " +
				"at 15:04, and every 15 minutes after that while it keeps exiting. To bring it back yourself, press Ctrl-C in baton's terminal and run `baton --resume sess-1`."},
		{"stopped by the human", RecoveryStopped("sess-1"), "you stopped baton bringing claude back. To go on, run `baton --resume sess-1`."},
		{"resumed, refused", ResumedQuietly("claude exited with code 1"),
			"baton: claude exited with code 1 mid-plan → resumed this conversation; its last turn stands refused, so baton leaves it to you: rewind (Esc Esc), or /clear for a fresh one"},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

// An API error a fresh session avoids is told as Claude Code reported it, its details first, on one line
// and cut short; the fresh session, the human and the roster each hear it in their own words.
func TestAFailedTurnStartsAfresh(t *testing.T) {
	long := strings.Repeat("x", 250)
	for _, c := range []struct {
		name string
		e    state.StopError
		want string
	}{
		{"details", state.StopError{Error: "invalid_request", Details: "prompt is too long: 213456 tokens > 200000 maximum", Message: "Prompt is too long"},
			"the last turn failed with an API error (invalid_request: prompt is too long: 213456 tokens > 200000 maximum)"},
		{"only Claude Code's message", state.StopError{Error: "invalid_request", Message: "Autocompact is thrashing:\n the context refilled"},
			"the last turn failed with an API error (invalid_request: Autocompact is thrashing: the context refilled)"},
		{"nothing said", state.StopError{Error: "invalid_request"}, "the last turn failed with an API error (invalid_request)"},
		{"said at length", state.StopError{Error: "invalid_request", Details: long},
			"the last turn failed with an API error (invalid_request: " + long[:199] + "…)"},
	} {
		if got := FailedTurn(c.e); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}

	why := "the last turn failed with an API error (invalid_request: prompt is too long)"
	at := time.Date(2026, 10, 15, 15, 4, 0, 0, time.Local)
	for _, c := range []struct{ name, got, want string }{
		{"the fresh session's prompt", Words{Git: true}.Afresh("P3", why),
			"[baton] In the conversation before this one, the last turn failed with an API error (invalid_request: prompt is too long), which every turn of it would have met again, " +
				"so baton started this fresh session: the brief above has P3's instructions. Continue P3 from there: some of it may be done already, " +
				"so check what is (`git log`, `git status`, and the notes the phase left) before you redo anything."},
		{"with no phase in hand", Words{}.Afresh("", why),
			"[baton] In the conversation before this one, the last turn failed with an API error (invalid_request: prompt is too long), which every turn of it would have met again, " +
				"so baton started this fresh session: the context above says where the plan stands. Go on from there."},
		{"the line", AfreshLine(why, "P3"), "baton: the last turn failed with an API error (invalid_request: prompt is too long), which a fresh session avoids → started a fresh session from P3's brief"},
		{"the line, no phase", AfreshLine(why, ""), "baton: the last turn failed with an API error (invalid_request: prompt is too long), which a fresh session avoids → started a fresh session for the run"},
		{"the roster", AgentFailedTurn(why), "its agent's last turn failed with an API error (invalid_request: prompt is too long), which a fresh agent avoids"},
		{"the lead, later", AfreshLater(Stop{Phase: "P3"}, why, at, 30*time.Minute),
			"P3: the last turn failed with an API error (invalid_request: prompt is too long) once more, which a fresh session avoids. baton has started fresh ones often in the last 30 minutes, so it starts the next at 15:04."},
		{"a lead with no phase, later", AfreshLater(Stop{}, why, at, 30*time.Minute),
			"the lead session: the last turn failed with an API error (invalid_request: prompt is too long) once more, which a fresh session avoids. baton has started fresh ones often in the last 30 minutes, so it starts the next at 15:04."},
		{"an agent, later", AfreshLater(Stop{Phase: "P3", Worker: true}, why, at, 30*time.Minute),
			"the phase agent on P3: the last turn failed with an API error (invalid_request: prompt is too long) once more, which a fresh agent avoids. baton has started fresh ones often in the last 30 minutes, so it starts the next at 15:04."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}
