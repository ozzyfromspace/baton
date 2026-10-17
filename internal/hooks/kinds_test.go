package hooks

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The phase in hand waits on its gate and its wait on something outside, side by side: its stop is
// allowed, naming both, and logged with both.
func TestAStopOnAGateAndAWait(t *testing.T) {
	st := running()
	st.Jobs = []state.Job{
		{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make qa", Status: state.JobRunning},
		{ID: "j2", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch 7 --exit-status", Status: state.JobQueued},
	}
	d := decideStop(&st, demoPlan(), tStop, decide.Words{Git: true}, nil)
	if d.output["decision"] == "block" {
		t.Fatalf("refused: %v", d.output)
	}
	if msg, _ := d.output["systemMessage"].(string); !strings.Contains(msg, "P0 (First) waits on jobs j1 (`make qa`) and j2 (`gh run watch 7 --exit-status`) — baton needs nothing from you for this wait") {
		t.Errorf("said %q", msg)
	}
	var logged bool
	for _, e := range d.events {
		logged = logged || e.kind == "idle_on_job" && e.fields["job"] == "j2" && e.fields["beside"] == "j1"
	}
	if !logged {
		t.Errorf("events %+v", d.events)
	}
}
