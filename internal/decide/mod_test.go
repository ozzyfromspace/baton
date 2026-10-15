package decide

import (
	"strings"
	"testing"
)

// Claude Code shows a kept report as "Prompt dropped by a hook: <this>", which reads like an error: the
// line says that it is the usual course, and needs nothing.
func TestAKeptReportReadsAsRoutine(t *testing.T) {
	got := ReportKept("P3")
	if got != "P3's report went to baton as usual — nothing for Claude to do" {
		t.Errorf("%q", got)
	}
	for _, alarming := range []string{"error", "failed", "not to Claude"} {
		if strings.Contains(got, alarming) {
			t.Errorf("%q says %q", got, alarming)
		}
	}
}
