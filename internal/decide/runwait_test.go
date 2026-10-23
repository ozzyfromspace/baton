package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A block's question tells the human what the rest of the run does meanwhile: it goes on, or nothing else
// in it can move until the block is cleared, naming the phases that wait on it. Any other question says
// neither.
func TestABlockQuestionSaysWhetherTheRunWaits(t *testing.T) {
	deadline := time.Date(2026, 10, 23, 5, 36, 0, 0, time.Local)
	block := state.Relay{Phase: "P6", Kind: state.RelayBlocked, Because: "the merge was refused twice."}
	many := []string{"P7", "P8", "P9", "P10", "P11", "P12", "P13", "P14", "P15", "P16"}
	for _, c := range []struct {
		name string
		r    state.Relay
		w    RunWait
		says string // between the block's reason and its deadline ("" for a question that is no block)
	}{
		{"the rest goes on", block, RunWait{}, "Only P6 waits for you; the rest of the run goes on."},
		{"the rest goes on, whatever waits behind", block, RunWait{Behind: []string{"P7"}}, "Only P6 waits for you; the rest of the run goes on."},
		{"one waits", block, RunWait{Stuck: true, Behind: []string{"P7"}}, "Nothing else in the run can move until it is cleared: P7 waits on P6."},
		{"three wait", block, RunWait{Stuck: true, Behind: []string{"P7", "P8", "P9"}}, "Nothing else in the run can move until it is cleared: P7, P8 and P9 wait on P6."},
		{"many wait", block, RunWait{Stuck: true, Behind: many},
			"Nothing else in the run can move until it is cleared: P7, P8, P9, P10, P11, P12, P13, P14 and 2 more wait on P6."},
		{"none behind it", block, RunWait{Stuck: true}, "Nothing else in the run can move until it is cleared."},
		{"a proposal", state.Relay{Phase: "P6", Kind: state.RelayProposal, Decision: "P6.d1", What: "use a fake clock", Because: "the clock drifts."},
			RunWait{Stuck: true, Behind: []string{"P7"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := RelayQuestion(c.r, deadline, c.w).Text
			if c.says == "" {
				if strings.Contains(got, "the rest of the run") || strings.Contains(got, "can move") || strings.Contains(got, "P7") {
					t.Errorf("a question that is no block speaks of the run:\n%s", got)
				}
				return
			}
			want := "baton: the agent on P6 is blocked: the merge was refused twice. " + c.says +
				" Unless you answer by 05:36, it looks again for a safe way around it. To write to the agent, use @P6 <your words> at the lead's prompt."
			if got != want {
				t.Errorf("\n%s\nwant\n%s", got, want)
			}
		})
	}
	if len(many) != 10 || many[8] != "P15" {
		t.Errorf("the phases behind were changed: %q", many)
	}
}
