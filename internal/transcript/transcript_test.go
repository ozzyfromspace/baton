package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata are shaped like the entries recorded in real refusals
// (docs/research/recovery.md), with none of their content.
func TestRefused(t *testing.T) {
	cyber := &Refusal{Category: "cyber", Explanation: "This request triggered restrictions."}
	for _, c := range []struct {
		file  string
		agent bool // a subagent's own transcript, every entry of it a sidechain's
		want  *Refusal
	}{
		// Claude Code went on once by itself, the safeguards stopped that too, and it would not go on.
		{"refused.jsonl", false, cyber},
		{"refused-system.jsonl", false, cyber},
		{"refused-assistant.jsonl", false, cyber},
		// The API sends null for a category it does not name.
		{"refused-unnamed.jsonl", false, &Refusal{Explanation: "This request triggered restrictions."}},
		// A turn after the refusal ended some other way.
		{"refused-then-prompt.jsonl", false, nil},
		// So did one whose prompt the transcript did not mark (no promptSource): the model replied since.
		{"refused-then-reply.jsonl", false, nil},
		// The safeguards stopped a response, and the turn went on after Claude Code's own retry.
		{"continued.jsonl", false, nil},
		// A last line cut off is skipped; the refusal before it still counts.
		{"cut.jsonl", false, cyber},
		// A subagent's entries say nothing about the main turn.
		{"subagent.jsonl", false, nil},
		// A phase agent's own transcript, every entry of it a sidechain's: its refusal is its own, and its
		// entries are some subagent's, not the session's, read as a session's transcript.
		{"agent-refused.jsonl", true, cyber},
		{"agent-refused.jsonl", false, nil},
		{"refused.jsonl", true, nil},
	} {
		t.Run(c.file, func(t *testing.T) {
			got, err := Refused(filepath.Join("testdata", c.file), c.agent)
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestRefusedOnAMissingTranscript(t *testing.T) {
	got, err := Refused(filepath.Join(t.TempDir(), "gone.jsonl"), false)
	if got != nil || err == nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := Refused("", true); got != nil || err == nil {
		t.Fatalf("no path: got %+v, %v", got, err)
	}
}

// A path that names a directory is no transcript on any system: Windows opens one as an empty file.
func TestADirectoryIsNoTranscript(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		read func() error
	}{
		{"Tail", func() error { _, err := Tail(dir, TailBytes); return err }},
		{"Compacted", func() error { _, _, err := Compacted(dir, 0, false); return err }},
		{"Leaf", func() error { _, err := Leaf(dir); return err }},
		{"Forked", func() error { _, err := Forked(dir, Mark{}); return err }},
	} {
		if err := c.read(); !errors.Is(err, ErrNotFile) {
			t.Errorf("%s: %v, want ErrNotFile", c.name, err)
		}
	}
}

// A conversation's context is the input of its last reply, or what a compaction since left it.
func TestContext(t *testing.T) {
	for _, c := range []struct {
		file   string
		agent  bool
		tokens int
		at     string
	}{
		{"agent-context.jsonl", true, 241129, "12:01:00.5"}, // the last reply's, written once per content block
		{"agent-compacted.jsonl", true, 14586, "12:02:00"},  // the compaction's since
		{"agent-after.jsonl", true, 44462, "12:02:30"},
		{"agent-context.jsonl", false, 0, ""}, // a subagent's entries, read as a session's transcript
		{"refused.jsonl", false, 0, ""},       // replies with no usage
	} {
		t.Run(c.file, func(t *testing.T) {
			tokens, at, err := Context(filepath.Join("testdata", c.file), c.agent)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if !at.IsZero() {
				want = at.UTC().Format("15:04:05.9")
			}
			if tokens != c.tokens || want != c.at {
				t.Fatalf("%d at %q, want %d at %q", tokens, want, c.tokens, c.at)
			}
		})
	}
	if _, _, err := Context(filepath.Join(t.TempDir(), "gone.jsonl"), true); err == nil {
		t.Fatal("no error for a transcript that is not there")
	}
}

// A tail that starts inside a line skips it, and only whole entries are read.
func TestTail(t *testing.T) {
	path := filepath.Join("testdata", "refused.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	all, err := Tail(path, int64(len(data)))
	if err != nil || len(all) != 9 {
		t.Fatalf("whole file: %d entries, %v", len(all), err)
	}
	if e := all[0]; !e.Prompt() || e.UUID != "u1" || e.Parent != "a0" || e.Source != "typed" {
		t.Errorf("the prompt: %+v", e)
	}
	if e := all[3]; e.Prompt() || !e.Meta || e.Type != "user" {
		t.Errorf("Claude Code's own text is not a prompt: %+v", e)
	}
	if all[1].Refusal != nil || all[4].Refusal != nil {
		t.Error("a response the safeguards stopped, that Claude Code may go on from, is taken for the end of the turn")
	}
	if e := all[5]; e.Type != "system" || e.Subtype != "model_refusal_no_fallback" || e.Refusal == nil {
		t.Errorf("the system entry: %+v", e)
	}
	if e := all[8]; e.Type != "last-prompt" || e.UUID != "" {
		t.Errorf("the last-prompt entry: %+v", e)
	}
	last := strings.LastIndex(strings.TrimSuffix(string(data), "\n"), "\n") + 1
	tail, err := Tail(path, int64(len(data)-last+5)) // the last line and a bit of the one before it
	if err != nil || len(tail) != 1 || tail[0].Type != "last-prompt" {
		t.Fatalf("tail: %+v, %v", tail, err)
	}
	if none, err := Tail(path, 10); err != nil || len(none) != 0 {
		t.Fatalf("a tail inside one line: %+v, %v", none, err)
	}
}

// A compact boundary's parent is its logicalParentUuid.
func TestLogicalParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(`{"type":"system","subtype":"compact_boundary","parentUuid":null,"logicalParentUuid":"a9","uuid":"b1"}`+"\n"), 0o644)
	got, err := Tail(path, TailBytes)
	if err != nil || len(got) != 1 || got[0].Parent != "a9" {
		t.Fatalf("%+v, %v", got, err)
	}
}
