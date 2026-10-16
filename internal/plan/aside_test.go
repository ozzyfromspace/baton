package plan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestAsideName(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"The next campaign", "the-next-campaign.md"},
		{"baton v0.8.6 — room to work before a checkpoint, the plan sent once, and what a real night's run showed",
			"baton-v0-8-6-room-to-work-before-a-checkpoint-the-plan-sent.md"},
		{"  Fix: the login bug!  ", "fix-the-login-bug.md"},
		{"Ünïcode Ärger über Größen", "ünïcode-ärger-über-größen.md"},
		{"Don’t panic", "dont-panic.md"},
		{"a/b\\c:d*e?f\"g<h>i|j", "a-b-c-d-e-f-g-h-i-j.md"},
		{"— ? —", "plan.md"},
		{"", "plan.md"},
		{"Supercalifragilisticexpialidociouslyandmoreandmoreandmoreandmorewords", "supercalifragilisticexpialidociouslyandmoreandmoreandmoreand.md"},
	} {
		if got := AsideName(c.title); got != c.want {
			t.Errorf("AsideName(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

// A plan is moved in its folder to a name from its title, never over a file there, and its text goes with
// it; nothing moves a file that is not there.
func TestMoveAside(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	first := write("humming-whistling-hare.md", "# The next campaign\n")
	to, err := MoveAside(first, "The next campaign")
	if err != nil || to != filepath.Join(dir, "the-next-campaign.md") || read(to) != "# The next campaign\n" {
		t.Fatalf("moved to %q (%v), reading %q", to, err, read(to))
	}
	if _, err := os.Stat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the plan-mode file is still there: %v", err)
	}

	// Claude Code writes the plan-mode file afresh at the next planning: a plan of the same title takes
	// the next free name, and the one moved before keeps its text.
	again := write("humming-whistling-hare.md", "# The next campaign, revised\n")
	write("the-next-campaign-2.md", "someone else's\n")
	to2, err := MoveAside(again, "The next campaign")
	if err != nil || to2 != filepath.Join(dir, "the-next-campaign-3.md") || read(to2) != "# The next campaign, revised\n" {
		t.Fatalf("moved to %q (%v)", to2, err)
	}
	if read(to) != "# The next campaign\n" || read(filepath.Join(dir, "the-next-campaign-2.md")) != "someone else's\n" {
		t.Fatal("a file already there was changed")
	}

	// A title that is the plan-mode file's own name moves it to the next one.
	same := write("plan-this.md", "# Plan this\n")
	if to, err := MoveAside(same, "Plan this"); err != nil || to != filepath.Join(dir, "plan-this-2.md") {
		t.Fatalf("moved to %q (%v)", to, err)
	}

	if to, err := MoveAside(filepath.Join(dir, "gone.md"), "Gone"); err == nil || to != "" {
		t.Fatalf("moved a missing file to %q", to)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("made a file")
	}
}
