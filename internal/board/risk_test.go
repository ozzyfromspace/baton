package board

import (
	"slices"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The board, and baton watch with it, shows the risk ledger's total once it has scored a phase, and a hold.
func TestBoardShowsTheRisk(t *testing.T) {
	r := newRun()
	if b := r.board(""); b.Risk != nil || slices.ContainsFunc(b.Lines(0), func(l string) bool { return l == decide.RiskLine(0, false) }) {
		t.Errorf("a ledger with nothing scored shows: %+v", b.Risk)
	}
	state.AddRisk(&r.st, state.PhaseRisk{Phase: "P1", Points: 13})
	b := r.board("")
	if b.Risk == nil || b.Risk.Total != 13 || b.Risk.Held || !slices.Contains(b.Lines(0), decide.RiskLine(13, false)) {
		t.Errorf("risk %+v, lines %q", b.Risk, b.Lines(0))
	}
	state.RiskVerdict(&r.st, state.VerdictFull, now)
	if b := r.board(""); !b.Risk.Held || !slices.Contains(b.Lines(0), decide.RiskLine(13, true)) {
		t.Errorf("held: risk %+v, lines %q", b.Risk, b.Lines(0))
	}
}
