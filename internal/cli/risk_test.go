package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton done records the deviation it is given, the lead's and a phase agent's, and logs it; without one,
// it asks nothing and records nothing, so the risk ledger counts it as unknown.
func TestDoneRecordsTheDeviation(t *testing.T) {
	for _, c := range []struct {
		name      string
		agent     bool
		deviation string
	}{
		{"the lead's, with one", false, "split the store in two"},
		{"a phase agent's, with one", true, "none"},
		{"the lead's, without", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead := asWorker(t)
			s, phase := lead, "P0"
			if c.agent {
				s, phase = w, "P1"
			}
			args := []string{"done", phase, "--notes", "Done."}
			if c.deviation != "" {
				args = append(args, "--deviation", c.deviation)
			}
			out := s.must("", args...)
			if strings.Contains(out, "deviation") {
				t.Errorf("done says something of the deviation:\n%s", out)
			}
			if got := s.state().Phases[phase].Deviation; got != c.deviation {
				t.Errorf("%s's deviation is %q, want %q", phase, got, c.deviation)
			}
			if logged := s.logged("phase_done", map[string]any{"phase": phase, "deviation": c.deviation}); logged != (c.deviation != "") {
				t.Errorf("phase_done logs the deviation: %v\n%s", logged, s.events())
			}
		})
	}
}

// baton done's help asks for the deviation.
func TestDoneHelpAsksForTheDeviation(t *testing.T) {
	_, lead := asWorker(t)
	code, _, errs := lead.run("", "done")
	if code == 0 || !strings.Contains(errs, decide.DeviationHelp) {
		t.Errorf("done with no phase: %d\n%s", code, errs)
	}
}

// A phase finished over its red verification is marked so for the ledger; --despite-red over a gate that
// is not red marks nothing.
func TestDespiteRedMarksThePhase(t *testing.T) {
	for _, red := range []bool{true, false} {
		s := afterSession(t)
		status := state.JobPassed
		if red {
			status = state.JobFailed
		}
		s.job(status, 1)
		s.must("", "done", "P0", "--despite-red", "the link checker needs the network")
		if st, _ := s.store().Load(); st.FinishedDespiteRed("P0") != red {
			t.Errorf("red %v: marked %v", red, st.FinishedDespiteRed("P0"))
		}
	}
}

// recordDone marks a phase finished over a red verification for the ledger; baton status shows the ledger
// once it has scored a phase, and whether new phases are held.
func TestRecordDoneAndStatusLine(t *testing.T) {
	st := state.State{Phases: map[string]*state.PhaseState{"P1": {Deviation: "earlier"}}}
	recordDone(&st, "P1", "", true)
	if !st.FinishedDespiteRed("P1") || st.Phases["P1"].Deviation != "earlier" {
		t.Errorf("recordDone: %+v, %+v", st.Phases["P1"], st.Risk)
	}
	var b strings.Builder
	printRisk(IO{Out: &b}, state.State{})
	if b.Len() != 0 {
		t.Errorf("a ledger with nothing scored: %q", b.String())
	}
	st.Risk.Scored, st.Risk.Total, st.Risk.Held = []string{"P1"}, 17, time.Now()
	printRisk(IO{Out: &b}, st)
	if got := b.String(); got != decide.RiskLine(17, true)+"\n" {
		t.Errorf("status line = %q", got)
	}
}
