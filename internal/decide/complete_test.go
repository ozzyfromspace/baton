package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The message that wakes the lead when a phase agent's landing completed the plan says so, and asks for the
// summary its own last done would have.
func TestThePlanCompleteMailAsksForTheSummary(t *testing.T) {
	note := state.Decision{ID: "P2.d1", Kind: state.Note, What: "used a fake clock"}
	cases := []struct {
		phases int
		saving state.Saving
		ds     []state.Decision
		want   string
	}{
		{1, state.Saving{}, nil, "[baton] P0's work landed on the run's branch, and it was the last thing the plan waited for: the plan is complete, its 1 phase done. " +
			"Summarize the results for the human, and end your turn."},
		{4, state.Saving{Work: 4 * time.Hour, Wall: 3 * time.Hour, Agents: 2}, []state.Decision{note},
			"[baton] P0's work landed on the run's branch, and it was the last thing the plan waited for: the plan is complete, all 4 phases done " +
				"(phase agents saved about 1h00m: 4h00m of phase work in 3h00m). Summarize the results for the human, including the 1 decision this run made " +
				"without them (baton status lists them, each with its undo), and end your turn."},
	}
	for _, c := range cases {
		if got := PlanCompleteMail("P0", c.phases, c.saving, c.ds); got != c.want {
			t.Errorf("PlanCompleteMail(%d) =\n%q\nwant\n%q", c.phases, got, c.want)
		}
	}
	if got := MailTyped([]string{state.MessageDone}); got != MailDone {
		t.Errorf("MailTyped = %q", got)
	}
}
