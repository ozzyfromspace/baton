package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every reply's entry says the model that answered and the effort its request was sent with (spike 43, Claude
// Code 2.1.318): LastServed reads the last of the conversation's own, passing over Claude Code's synthetic
// replies and, in a session's transcript, its subagents' entries. These lines are cut down from spike 43's.
func TestLastServed(t *testing.T) {
	reply := func(model, effort, at string, extra string) string {
		return `{"type":"assistant","timestamp":"` + at + `","requestedModel":"` + model + `","effort":"` + effort + `"` + extra +
			`,"message":{"model":"` + model + `","role":"assistant","content":[{"type":"text","text":"DONE"}],"usage":{"input_tokens":3}}}`
	}
	prompt := `{"type":"user","promptSource":"typed","timestamp":"2026-11-04T06:40:07.000Z","message":{"role":"user","content":"go"}}`
	for _, c := range []struct {
		name  string
		lines []string
		agent bool
		want  *Served
	}{
		{"one reply", []string{prompt, reply("claude-haiku-5-5", "low", "2026-11-04T06:35:56.646Z", "")}, false,
			&Served{Model: "claude-haiku-5-5", Requested: "claude-haiku-5-5", Effort: "low"}},
		{"the last of two: lowered on a resume", []string{reply("claude-haiku-5-5", "max", "2026-11-04T06:39:13.505Z", ""), prompt,
			reply("claude-haiku-5-5", "low", "2026-11-04T06:40:08.245Z", "")}, false,
			&Served{Model: "claude-haiku-5-5", Requested: "claude-haiku-5-5", Effort: "low"}},
		{"a synthetic reply after it", []string{reply("claude-sonnet-5-5", "high", "2026-11-04T06:37:03.011Z", ""),
			`{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>","content":[]}}`}, false,
			&Served{Model: "claude-sonnet-5-5", Requested: "claude-sonnet-5-5", Effort: "high"}},
		{"a subagent's entries in the session's transcript", []string{reply("claude-opus-5-5", "xhigh", "2026-11-04T06:36:00.000Z", ""),
			reply("claude-haiku-5-5", "max", "2026-11-04T06:36:06.000Z", `,"isSidechain":true`)}, false,
			&Served{Model: "claude-opus-5-5", Requested: "claude-opus-5-5", Effort: "xhigh"}},
		{"an agent's own transcript", []string{reply("claude-sonnet-5-5", "low", "2026-11-04T06:39:21.345Z", `,"isSidechain":true`)}, true,
			&Served{Model: "claude-sonnet-5-5", Requested: "claude-sonnet-5-5", Effort: "low"}},
		{"no reply yet", []string{prompt}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(c.lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := LastServed(path, c.agent)
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == nil) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if got != nil && (got.Model != c.want.Model || got.Requested != c.want.Requested || got.Effort != c.want.Effort || got.At.IsZero()) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
	if _, err := LastServed(filepath.Join(t.TempDir(), "missing.jsonl"), false); err == nil {
		t.Error("a missing transcript read as one with no reply")
	}
}
