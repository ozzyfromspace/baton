package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The briefs ask for a phase's deviation up front, as it reports itself done, the lead's and an agent's.
func TestBriefsAskForTheDeviation(t *testing.T) {
	for _, w := range []Words{{Git: true}, {Git: true, Worker: true}} {
		if r := w.Reporting(); !strings.Contains(r, DoneCall) || !strings.Contains(r, "--deviation \"<how it departed from the plan, or none>\"") {
			t.Errorf("worker %v: the reporting words ask for no deviation:\n%s", w.Worker, r)
		}
	}
}

// The look-in gives the score and its factors, holding nothing; the hold says why, how to lift it, the
// landings a check in the checkout would hold, and when baton lifts it itself.
func TestRiskWords(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)
	r := state.Risk{Total: 31, Phases: []state.PhaseRisk{
		{Phase: "P3", Points: 9, Factors: []state.RiskFactor{{Name: "lines", Points: 3, Detail: "612 lines changed"}, {Name: "no_check", Points: 4}}},
		{Phase: "P5", Points: 22, Factors: []state.RiskFactor{{Name: "despite_red", Points: 5}}},
	}}
	look := RiskLookIn(r, state.RiskReview, 24)
	for _, want := range []string{"31 points", "review line (24)", "nothing is held", "P5, 22 points: despite red 5", "lines 3 (612 lines changed), no check 4", "baton verify --qa"} {
		if !strings.Contains(look, want) {
			t.Errorf("the look-in lacks %q:\n%s", want, look)
		}
	}
	if strings.Index(look, "P5") > strings.Index(look, "P3") {
		t.Errorf("the look-in does not put the riskiest phase first:\n%s", look)
	}

	r.Verdict = state.VerdictFull
	for _, c := range []struct {
		landing, working []string
		want             string
	}{
		{nil, nil, "would hold every landing"},
		{[]string{"P4"}, nil, "the landings of P4, which waits to land"},
		{nil, []string{"P6", "P7"}, "the landings of P6 and P7, whose agents are at work"},
		{[]string{"P4"}, []string{"P6"}, "the landings of P4, which waits to land, and of P6, whose agent is at work"},
	} {
		held := RiskHeld(r, c.landing, c.working, t0.Add(10*time.Minute))
		for _, want := range []string{c.want, "the risk assessment says a full check is due now", "baton verify --qa", "in a lane rather than --here", "baton note", "09:10"} {
			if !strings.Contains(held, want) {
				t.Errorf("the hold lacks %q:\n%s", want, held)
			}
		}
	}
	if s := RiskLine(1, false); s != "risk: 1 point since the last full check" {
		t.Errorf("RiskLine = %q", s)
	}
	if s := RiskLine(30, true); !strings.HasSuffix(s, "new phases held for a full check") {
		t.Errorf("RiskLine held = %q", s)
	}
	for by, want := range map[string]string{RiskByVerify: "job j9", RiskByNote: "j9", RiskByCleared: "passed", RiskByDeadline: "deadline"} {
		if s := RiskReleasedSay(by, "j9"); !strings.Contains(s, want) || !strings.Contains(s, "new phases may start again") {
			t.Errorf("RiskReleasedSay(%s) = %q", by, s)
		}
	}
}
