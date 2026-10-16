package transcript

import (
	"path/filepath"
	"testing"
)

// The session's plan slug is the latest its own entries carry: it changed once in this one, and a
// subagent's entry after it does not count.
func TestSlug(t *testing.T) {
	for _, c := range []struct {
		file, want string
	}{
		{"slugged.jsonl", "harmonic-mapping-candy"},
		{"turns.jsonl", ""}, // a session that never planned carries none
	} {
		got, err := Slug(filepath.Join("testdata", c.file))
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.file, got, err, c.want)
		}
	}
	if got, err := Slug(filepath.Join(t.TempDir(), "gone.jsonl")); got != "" || err == nil {
		t.Errorf("a missing transcript: got %q, %v", got, err)
	}
}
