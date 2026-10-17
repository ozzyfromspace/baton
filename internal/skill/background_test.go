package skill

import (
	"strings"
	"testing"
)

// longGates is the text of the skill's section on long gates and background work.
func longGates(t *testing.T) string {
	t.Helper()
	text := strings.ReplaceAll(Text, "\r\n", "\n")
	start := strings.Index(text, "\n### Long gates and background work\n")
	if start < 0 {
		t.Fatal("the skill has no section on long gates")
	}
	section := text[start+1:]
	if end := strings.Index(section[1:], "\n### "); end >= 0 {
		section = section[:end+1]
	}
	return section
}

// The skill says what baton says as background work starts: Claude Code wakes the model when it ends, so
// no loop, sleep or other task is started only to wait for it.
func TestNoPollerForBackgroundWork(t *testing.T) {
	section := longGates(t)
	if want := "Never start a loop, a `sleep` or another background task only to wait for one that runs: Claude Code wakes you when it ends."; !strings.Contains(section, want) {
		t.Errorf("the section lacks %q:\n%s", want, section)
	}
}

// The skill gives the gate protocol in one sentence: a long gate is a committed script of baton stages,
// which stops at its first failure, and after a red, baton stop ends it and --failed reruns only what
// failed.
func TestLongGatesAreStagedAndRerunByWhatFailed(t *testing.T) {
	section := longGates(t)
	if want := "- Build a long gate as a committed script of `baton stage <name> -- <command>` steps, which streams each stage and stops at the first failure; " +
		"when a gate fails, end it with `baton stop <job>` and rerun only what failed with `baton verify --failed`, never the full suite."; !strings.Contains(section, want) {
		t.Errorf("the section lacks %q:\n%s", want, section)
	}
	if want := "A red gate stands until the same command, or its `baton verify --failed` rerun, passes"; !strings.Contains(Text, want) {
		t.Errorf("the skill lacks %q", want)
	}
}
