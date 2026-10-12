package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The conversation's last compact boundary after an offset, and where reading stopped.
func TestCompacted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "compacted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	at := int64(strings.Index(text, `{"parentUuid":null,"logicalParentUuid":"s1"`)) // the boundary's line
	end := at + int64(strings.Index(text[at:], "\n")) + 1
	sidechain := `{"type":"system","subtype":"compact_boundary","isSidechain":true,"uuid":"sc"}` + "\n"
	auto := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto"},"uuid":"b2","timestamp":"2026-10-12T13:00:00.000Z"}`
	for _, c := range []struct {
		name     string
		text     string
		agent    bool
		from     int64
		want     string // the boundary's uuid ("" for none)
		trigger  string
		next     int64 // -1: the end of the text
		start    int64
		finished int64
	}{
		{"from the start", text, false, 0, "cb", "manual", -1, at, end},
		{"from just before it", text, false, at, "cb", "manual", -1, at, end},
		{"from inside its line", text, false, at + 3, "", "", -1, 0, 0},
		{"from past it", text, false, end, "", "", -1, 0, 0},
		{"a subagent's boundary", sidechain, false, 0, "", "", -1, 0, 0},
		// In a subagent's own transcript, its own boundary is a sidechain's, and the session's is none of its.
		{"a subagent's own", sidechain, true, 0, "sc", "", -1, 0, int64(len(sidechain))},
		{"the session's, read as a subagent's", text, true, 0, "", "", -1, 0, 0},
		{"the last of two", text + auto + "\n", false, 0, "b2", "auto", -1, int64(len(text)), int64(len(text) + len(auto) + 1)},
		// Still being written: not read, and read from its start next time.
		{"a line not yet ended", text + auto, false, end, "", "", int64(len(text)), 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			os.WriteFile(path, []byte(c.text), 0o644)
			b, next, err := Compacted(path, c.from, c.agent)
			if err != nil {
				t.Fatal(err)
			}
			if want := c.next; want < 0 && next != int64(len(c.text)) || want >= 0 && next != want {
				t.Errorf("next %d, want %d (of %d)", next, c.next, len(c.text))
			}
			if c.want == "" {
				if b != nil {
					t.Fatalf("a boundary: %+v", b)
				}
				return
			}
			if b == nil || b.UUID != c.want || b.Trigger != c.trigger || b.Start != c.start || b.End != c.finished || b.Time.IsZero() != (c.trigger == "") {
				t.Fatalf("%+v, want %s (%s) at %d–%d", b, c.want, c.trigger, c.start, c.finished)
			}
		})
	}
}

// A transcript that is shorter than the offset is not the one it was taken in, and one that is not there
// says so.
func TestCompactedErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(`{"type":"user","uuid":"u1"}`+"\n"), 0o644)
	if _, _, err := Compacted(path, 1000, false); !errors.Is(err, ErrShrunk) {
		t.Errorf("shrunk: %v", err)
	}
	if _, next, err := Compacted(filepath.Join(t.TempDir(), "none.jsonl"), 7, true); err == nil || next != 7 {
		t.Errorf("missing: %d, %v", next, err)
	}
}
