package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// A turn that ended on a usage limit is recorded with its window and reset, read from the loop's own
// transcript, since StopFailure's input says neither. A 429 the transcript names no window for, a missing
// transcript, and a reader that panics leave the error as Claude Code reported it.
func TestAUsageStopKeepsItsWindowAndReset(t *testing.T) {
	testdata := filepath.Join("..", "transcript", "testdata")
	five := time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)
	week := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, transcript string
		panics           bool
		window           string
		resets           time.Time
	}{
		{"the five-hour window", "limit-five-hour.jsonl", false, state.WindowFiveHour, five},
		{"the weekly window", "limit-weekly.jsonl", false, state.WindowWeekly, week},
		{"a 429 of no window", "limit-unparsed.jsonl", false, "", time.Time{}},
		{"a turn since", "limit-then-prompt.jsonl", false, "", time.Time{}},
		{"no transcript", "", false, "", time.Time{}},
		{"a reader that panics", "limit-weekly.jsonl", true, "", time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.panics {
				defer func(r func(string, bool) (*transcript.Limit, error)) { readLimit = r }(readLimit)
				readLimit = func(string, bool) (*transcript.Limit, error) { panic("boom") }
			}
			f := newFixture(t, true)
			in := map[string]any{"error": "rate_limit", "last_assistant_message": "You've hit your limit"}
			if c.transcript != "" {
				in["transcript_path"] = filepath.Join(testdata, c.transcript)
			}
			f.fire("StopFailure", in) // fails the test if it exits other than 0, or fails open
			got := f.state().Run.LastError
			if got == nil || got.Error != "rate_limit" || got.Window != c.window || !got.Resets.Equal(c.resets) || !got.At.Equal(f.now) {
				t.Fatalf("recorded %+v, want %s %s", got, c.window, c.resets)
			}
			fields := map[string]any{"error": "rate_limit"}
			if c.window != "" {
				fields["window"], fields["resets"] = c.window, c.resets.Format(time.RFC3339)
			}
			if !f.logged("stop_failure", fields) {
				t.Fatalf("events:\n%s", f.eventLog())
			}
		})
	}
}

// A phase agent's usage stop is read from its own transcript, every entry of which is a sidechain's, and
// recorded in its sub-run.
func TestAPhaseAgentsUsageStopIsReadFromItsOwnTranscript(t *testing.T) {
	w, lead, _ := newWorker(t)
	dir := t.TempDir()
	mine := filepath.Join(dir, "sess-1", "subagents", "agent-agent-p2.jsonl")
	os.MkdirAll(filepath.Dir(mine), 0o755)
	os.WriteFile(mine, must(os.ReadFile(filepath.Join("..", "transcript", "testdata", "agent-limit.jsonl"))), 0o644)
	os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), must(os.ReadFile(filepath.Join("..", "transcript", "testdata", "limit-five-hour.jsonl"))), 0o644)
	w.fire("StopFailure", map[string]any{"error": "rate_limit", "transcript_path": filepath.Join(dir, "sess-1.jsonl")})
	week := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	if e := w.state().Run.LastError; e == nil || e.Window != state.WindowWeekly || !e.Resets.Equal(week) {
		t.Errorf("the sub-run recorded %+v", e)
	}
	if e := lead.state().Run.LastError; e != nil {
		t.Errorf("the lead recorded %+v", e)
	}
}
