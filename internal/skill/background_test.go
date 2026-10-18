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

// The skill gives the gate protocol in one bullet, opened by what the human's time is: the model does not
// feel a gate's time and the human does; a long gate is a committed script of baton stages, which stops at
// its first failure and tears down what it starts; after a red, baton stop ends it and --failed reruns
// only what failed, staged or by the narrower command; and the whole gate again takes a reason, told to
// the human.
func TestLongGatesAreStagedAndRerunByWhatFailed(t *testing.T) {
	section := longGates(t)
	if want := "- You do not feel the time a gate takes; the human does, and a check that cannot change what ships only spends it. " +
		"Build a long gate as a committed script of `baton stage <name> -- <command>` steps, which streams each stage, stops at the first failure and tears down what it starts (`trap … EXIT`); " +
		"when a gate fails, end it with `baton stop <job>` and rerun only what failed with `baton verify --failed` " +
		"(a gate without stages: `baton verify --failed -- <narrower command>`), never the full suite. " +
		"To run a whole gate again, say why with `--why \"…\"`: baton tells the human."; !strings.Contains(section, want) {
		t.Errorf("the section lacks %q:\n%s", want, section)
	}
	if want := "A red gate stands until the same command, or its `baton verify --failed` rerun, passes"; !strings.Contains(Text, want) {
		t.Errorf("the skill lacks %q", want)
	}
}
