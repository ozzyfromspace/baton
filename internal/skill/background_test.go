package skill

import (
	"strings"
	"testing"
)

// The skill says what baton says as background work starts: Claude Code wakes the model when it ends, so
// no loop, sleep or other task is started only to wait for it.
func TestNoPollerForBackgroundWork(t *testing.T) {
	text := strings.ReplaceAll(Text, "\r\n", "\n")
	start := strings.Index(text, "\n### Long gates and background work\n")
	if start < 0 {
		t.Fatal("the skill has no section on long gates")
	}
	section := text[start+1:]
	if end := strings.Index(section[1:], "\n### "); end >= 0 {
		section = section[:end+1]
	}
	if want := "Never start a loop, a `sleep` or another background task only to wait for one that runs: Claude Code wakes you when it ends."; !strings.Contains(section, want) {
		t.Errorf("the section lacks %q:\n%s", want, section)
	}
}
