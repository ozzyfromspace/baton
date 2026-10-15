package state

import (
	"testing"
	"time"
)

// The first recoveries in the window come at once; past them, the first waits first, and each after one
// that waited, every; a loop that stayed up for the window starts over.
func TestBackoff(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	const first, every = 5 * time.Minute, 15 * time.Minute
	ago := func(d, waited time.Duration) Recovery { return Recovery{At: now.Add(-d), Waited: waited} }
	for _, c := range []struct {
		name string
		rs   []Recovery
		want time.Duration
	}{
		{"none yet", nil, 0},
		{"one", []Recovery{ago(time.Minute, 0)}, 0},
		{"two in the window", []Recovery{ago(20*time.Minute, 0), ago(time.Minute, 0)}, first},
		{"two, one forgotten", []Recovery{ago(30*time.Minute, 0), ago(time.Minute, 0)}, 0},
		{"past the cap, one waited", []Recovery{ago(25*time.Minute, 0), ago(20*time.Minute, 0), ago(10*time.Minute, first)}, every},
		{"waited, and again", []Recovery{ago(16*time.Minute, every), ago(time.Minute, every)}, every},
		{"the one that waited forgotten", []Recovery{ago(40*time.Minute, every), ago(10*time.Minute, 0), ago(time.Minute, 0)}, first},
		{"up for the window since", []Recovery{ago(31*time.Minute, every), ago(45*time.Minute, every)}, 0},
	} {
		if got := Backoff(c.rs, now, first, every); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got := Recent([]Recovery{ago(31*time.Minute, 0), ago(29*time.Minute, 0)}, now); len(got) != 1 || !got[0].At.Equal(now.Add(-29*time.Minute)) {
		t.Errorf("recent: %+v", got)
	}
}

// An invalid request that is the conversation itself, as Claude Code words it in its details or its
// message, is avoided by a fresh session; every other error is not: a refusal, an expired login, billing,
// a model it may not use, the session's own settings, and anything else Claude Code calls invalid.
func TestErrorsAFreshSessionAvoids(t *testing.T) {
	for _, c := range []struct {
		name string
		e    StopError
		want bool
	}{
		{"prompt too long", StopError{Error: "invalid_request", Details: "prompt is too long: 213456 tokens > 200000 maximum", Message: "Prompt is too long"}, true},
		{"prompt too long, its message only", StopError{Error: "invalid_request", Message: "Prompt is too long"}, true},
		{"a compaction that failed for it", StopError{Error: "invalid_request", Message: "Prompt is too long · automatic compaction failed: timeout"}, true},
		{"input too long", StopError{Error: "invalid_request", Details: "Input is too long for requested model."}, true},
		{"a request too large", StopError{Error: "invalid_request", Details: "request_too_large: Request exceeds the maximum size"}, true},
		{"over the request limit", StopError{Error: "invalid_request", Details: "request_body_over_limit: body=33554432B (messages only) media=0B limit=33554432B"}, true},
		{"input and max_tokens over the context", StopError{Error: "invalid_request", Details: "input length and `max_tokens` exceed context limit: 198000 + 21333 > 200000"}, true},
		{"a compaction thrashing", StopError{Error: "invalid_request", Message: "Autocompact is thrashing: the context refilled to the limit within 3 turns of the previous compact, 3 times in a row."}, true},
		{"an orphaned tool result", StopError{Error: "invalid_request", Message: "API Error: 400 orphaned tool_result in conversation history. Run /rewind to recover the conversation."}, true},
		{"tool uses without results", StopError{Error: "invalid_request", Details: "messages.5: `tool_use` ids were found without `tool_result` blocks immediately after: toolu_01"}, true},
		{"an unexpected tool use id", StopError{Error: "invalid_request", Details: "messages.3.content.0: unexpected `tool_use_id` found in `tool_result` blocks: toolu_02"}, true},
		{"tool use concurrency", StopError{Error: "invalid_request", Message: "API Error: 400 due to tool use concurrency issues. Run /rewind to recover the conversation."}, true},
		{"an image too large", StopError{Error: "invalid_request", Message: "Image was too large. Try resizing the image or using a different approach."}, true},
		{"a PDF too large", StopError{Error: "invalid_request", Message: "PDF too large (max 100 pages, 32 MB). Try reading the file a different way."}, true},

		{"a refusal", StopError{Error: ErrRefusal, Details: "This request triggered restrictions on violative cyber content"}, false},
		{"a plain invalid request", StopError{Error: "invalid_request", Details: "bad request"}, false},
		{"nothing said", StopError{Error: "invalid_request"}, false},
		{"a model the plan has not", StopError{Error: "invalid_request", Message: "Claude Opus is not available with the Claude Pro plan."}, false},
		{"thinking off", StopError{Error: "invalid_request", Message: "API Error: effort is not supported with thinking turned off on this model"}, false},
		{"output tokens over the model's", StopError{Error: "invalid_request", Details: "max_tokens: 64001 > 64000, which is the maximum allowed number of output tokens for this model"}, false},
		{"an expired login", StopError{Error: "authentication_failed", Details: "please run /login"}, false},
		{"billing", StopError{Error: "billing_error", Message: "Credit balance is too low"}, false},
		{"an unknown model", StopError{Error: "model_not_found", Message: "There's an issue with the selected model"}, false},
		{"too long, but not invalid", StopError{Error: "server_error", Details: "prompt is too long"}, false},
	} {
		if got := c.e.Afresh(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A refused turn stands until a turn starts after it, or while its escalation is open.
func TestRefusedNow(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		run  Runtime
		want bool
	}{
		{"none", Runtime{}, false},
		{"refused", Runtime{LastError: &StopError{Error: ErrRefusal, At: at}, TurnStarted: at.Add(-time.Minute)}, true},
		{"a turn since", Runtime{LastError: &StopError{Error: ErrRefusal, At: at}, TurnStarted: at.Add(time.Minute)}, false},
		{"another error", Runtime{LastError: &StopError{Error: "invalid_request", At: at}}, false},
		{"its escalation open", Runtime{Escalation: &Escalation{Kind: "refused"}}, true},
		{"another escalation", Runtime{Escalation: &Escalation{Kind: "stalled"}}, false},
	} {
		st := New()
		st.Run = c.run
		if got := st.RefusedNow(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
