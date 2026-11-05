package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The brief of an agent given a phase the lead started says why it is the agent's (raised, or on its plan's
// tier), where the lead's work is, the lead's notes, and its last gate: a red one with what its log reports,
// a passing one in a line.
func TestHandedBrief(t *testing.T) {
	high, xhigh := plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "xhigh"}
	red := &HandedGate{Job: state.Job{ID: "j3", Phase: "P5", Command: "go test ./...", Status: state.JobFailed, Exit: 1}, Path: "/runs/j3.log",
		Failures: []string{"--- FAIL: TestParse"}, More: 2, Tail: "FAIL\tparse 0.1s"}
	passed := &HandedGate{Job: state.Job{ID: "j4", Phase: "P5", Command: "go test ./...", Status: state.JobPassed}}
	for _, c := range []struct {
		name      string
		h         Handover
		want, not []string
	}{
		{"raised, with commits and a red gate", Handover{Phase: "P5", Tier: xhigh, Lead: high, Raised: "3 red runs of its gate", Since: "0a2d601ffff",
			Commits: []string{"abc1234 feat: half a parser"}, More: 3, Notes: "the parser is half done", Gate: red}, []string{
			"The lead's session started P5, and baton gave it to you, on opus/xhigh: baton raised it to opus/xhigh after 3 red runs of its gate, and the lead runs on opus/high.",
			"Since it last took P5 up (at 0a2d601), it committed:\n\n  abc1234 feat: half a parser\n  … and 3 more",
			"The lead's notes as it set P5 aside:\n\nthe parser is half done",
			"Its last gate, job j3, ran `go test ./...` and failed (exit 1)",
			"--- FAIL: TestParse\n(and 2 more such lines)",
			"The end of its log (/runs/j3.log):\n\nFAIL\tparse 0.1s",
			"why its gate failed before you change more, then finish P5 here.",
		}, nil},
		{"its plan's tier, nothing committed, a gate that passed", Handover{Phase: "P5", Tier: xhigh, Lead: high, Since: "0a2d601", Gate: passed}, []string{
			"its tier is opus/xhigh, and the lead runs on opus/high.",
			"The lead committed nothing for P5 since it last took it up (at 0a2d601)",
			"Its last gate, job j4, ran `go test ./...` and passed",
			"Read what the lead did (git log, git show) before you change more",
		}, []string{"why its gate failed", "notes"}},
		{"no start on record, no gate", Handover{Phase: "P5", Tier: xhigh, Lead: high}, []string{
			"The lead's work on P5 is on the run's branch, which your branch starts from.",
		}, []string{"Its last gate", "committed"}},
	} {
		got := HandedBrief(c.h)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q in:\n%s", c.name, w, got)
			}
		}
	}
}

// The human is told what baton did with a phase it gave an agent, and what their session does next.
func TestPhaseHanded(t *testing.T) {
	high, max := plan.Tier{Model: "opus", Effort: "high"}, plan.Tier{Model: "opus", Effort: "max"}
	if got := PhaseHanded("P33", max, high, 6, "P34"); !strings.Contains(got, "could not restart on it while 6 phase agents were at work") ||
		!strings.HasSuffix(got, "your session takes up P34 after a compaction") {
		t.Errorf("PhaseHanded: %s", got)
	}
	if got := PhaseHanded("P33", max, high, 1, ""); !strings.Contains(got, "while 1 phase agent was at work") || !strings.HasSuffix(got, "holds no phase until one can start") {
		t.Errorf("PhaseHanded with nothing next: %s", got)
	}
	if got := BoundaryRedirected("P33", max, ""); !strings.Contains(got, "P33 goes to a phase agent on opus/max") {
		t.Errorf("BoundaryRedirected: %s", got)
	}
}
