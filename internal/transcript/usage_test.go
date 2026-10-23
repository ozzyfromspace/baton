package transcript

import (
	"path/filepath"
	"testing"
	"time"
)

// A turn that ended on one of the account's usage limits is written to the stopped loop's transcript with
// the window and its reset (docs/research/usage.md; these fixtures are made up in that shape: Claude Code
// 2.1.302 and later add apiError and rate_limit_info, earlier ones only quotaLimits).
func TestLimited(t *testing.T) {
	five := time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)
	week := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		file  string
		agent bool
		want  *Limit
	}{
		{"limit-five-hour.jsonl", false, &Limit{Kind: "five_hour", Resets: five}},
		{"limit-weekly.jsonl", false, &Limit{Kind: "seven_day", Resets: week}},
		// A 429 whose quota headers did not parse: a rate limit, not known to be a usage window.
		{"limit-unparsed.jsonl", false, &Limit{}},
		// A turn after the limit ended some other way.
		{"limit-then-prompt.jsonl", false, nil},
		// baton's retry met the limit again: the latest stop says when.
		{"limit-retried.jsonl", false, &Limit{Kind: "five_hour", Resets: time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC)}},
		// A phase agent's own transcript, every entry of it a sidechain's; read as a session's, it is some
		// subagent's and says nothing.
		{"agent-limit.jsonl", true, &Limit{Kind: "seven_day_opus", Resets: week}},
		{"agent-limit.jsonl", false, nil},
		// A refusal is no usage limit.
		{"refused.jsonl", false, nil},
	} {
		got, err := Limited(filepath.Join("testdata", c.file), c.agent)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if (got == nil) != (c.want == nil) || got != nil && (got.Kind != c.want.Kind || !got.Resets.Equal(c.want.Resets)) {
			t.Errorf("%s (agent %v): %+v, want %+v", c.file, c.agent, got, c.want)
		}
	}
}

// An entry whose error is not a string (a tool result's, say) still parses: baton reads the rest of it.
func TestAnErrorOfAnotherShapeKeepsItsEntry(t *testing.T) {
	e, ok := parse([]byte(`{"type":"user","uuid":"t1","promptSource":"typed","error":{"type":"tool_error"}}`))
	if !ok || e.UUID != "t1" || !e.Prompt() || e.Limit != nil {
		t.Fatalf("%+v %v", e, ok)
	}
}

func TestLimitedOnAMissingTranscript(t *testing.T) {
	if got, err := Limited(filepath.Join(t.TempDir(), "gone.jsonl"), false); got != nil || err == nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}
