package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestBoundaryWords(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	owed := func(fn func(st *state.State)) state.State {
		st := state.State{Mode: state.ModeRunning, Current: "P4", BoundaryOwed: true, Phases: map[string]*state.PhaseState{"P4": {Status: state.PhasePending}}}
		st.Run.Compaction.Queue("boundary", at)
		if fn != nil {
			fn(&st)
		}
		return st
	}
	parked := func(reason, job string) func(st *state.State) {
		return func(st *state.State) {
			st.Phases["P4"] = &state.PhaseState{Status: state.PhaseParked, Park: &state.Park{Reason: reason, Since: at, Job: job}}
			st.Jobs = []state.Job{{ID: "j31", Phase: "P4", Kind: state.JobVerify, Status: state.JobFailed, Exit: 2, Started: at.Add(-3 * time.Minute), Ended: at}}
		}
	}
	switched := func(st *state.State) {
		st.Run.Compaction.Queue("switch", at)
		st.Run.Compaction.Switch = &state.Switched{From: "P3", To: "P4", Job: "j8"}
	}
	for _, c := range []struct {
		name             string
		st               state.State
		hold, waits, say string
	}{
		{"a phase begins", owed(nil),
			"P4 begins after baton compacts the context",
			"[baton] P4 begins once this turn ends, after baton compacts the context: its brief comes then. Finish what this turn is doing, but do not start on P4 in it.",
			"baton: this turn runs to its end; then baton compacts the context, and P4 begins"},
		{"a phase set aside resumes", owed(parked(state.ParkJob, "j31")),
			"P4 resumes after baton compacts the context",
			"[baton] P4 resumes once this turn ends, after baton compacts the context: its brief comes then. Finish what this turn is doing, but do not start on P4 in it.",
			"baton: this turn runs to its end; then baton compacts the context, and P4 resumes"},
		{"a phase whose verification failed reopens", owed(parked(state.ParkRed, "j31")),
			"P4's verification failed (exit 2) after 3m (job j31), so P4 reopens after baton compacts the context",
			"[baton] P4's verification failed (exit 2) after 3m (job j31), so P4 reopens once this turn ends, after baton compacts the context: its brief comes then. Finish what this turn is doing, but do not start on P4 in it.",
			"baton: this turn runs to its end; then baton compacts the context, and P4 reopens"},
		{"a verification with no verdict", owed(parked(state.ParkUnverified, "j31")),
			"P4's verification failed (exit 2) after 3m (job j31), with no verdict, so P4 reopens after baton compacts the context", "", ""},
		{"a verification gone from the record", owed(parked(state.ParkRed, "j99")),
			"P4's verification is gone from the record, so P4 reopens after baton compacts the context", "", ""},
		{"a switch", owed(switched),
			"baton set P3 aside while its job j8 runs, and P4 begins after baton compacts the context",
			"[baton] baton set P3 aside while its job j8 runs, and P4 begins once this turn ends, after baton compacts the context: its brief comes then. Finish what this turn is doing, but do not start on P4 in it.",
			"baton: this turn runs to its end; then baton compacts the context, and P4 begins"},
	} {
		if got := BoundaryHold(c.st); got != c.hold {
			t.Errorf("%s: the hold\n got %q\nwant %q", c.name, got, c.hold)
		}
		if got := BoundaryWaits(c.st); c.waits != "" && got != c.waits {
			t.Errorf("%s: the model told\n got %q\nwant %q", c.name, got, c.waits)
		}
		if got := BoundaryWaitsSay(c.st); c.say != "" && got != c.say {
			t.Errorf("%s: the human told\n got %q\nwant %q", c.name, got, c.say)
		}
	}
}
