package cli

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/risk"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The risk ledger's part of the CLI (package risk, loop/risk.go): what `baton done` records for it, the
// line baton status shows, and the lines the host keeps it with.

// recordDone records what the done of phase id tells the risk ledger, once the done went through: how the
// phase departed from the plan, in the model's words (deviation; a done without one keeps what an earlier
// done of the phase said, and with none at all the ledger counts it as unknown: baton asks nothing), and
// that it was finished over a red verification (red).
func recordDone(st *state.State, id, deviation string, red bool) {
	if ps := st.Phases[id]; ps != nil && deviation != "" {
		ps.Deviation = deviation
	}
	if red {
		state.MarkDespiteRed(st, id)
	}
}

// printRisk is baton status's line for the ledger, once it has scored a phase.
func printRisk(io IO, st state.State) {
	if st.Risk == nil || len(st.Risk.Scored) == 0 && !st.RiskHeld() {
		return
	}
	fmt.Fprintln(io.Out, decide.RiskLine(st.RiskTotal(), st.RiskHeld()))
}

// riskLines is the ledger's lines, and the model that assesses it and how long it is waited for, as baton's
// config and the environment set them (config.LoadRisk).
func riskLines(io IO) (risk.Lines, error) {
	r, err := config.LoadRisk(batonRoot(io), io.Env)
	return risk.Lines{On: r.On, Review: r.Review, Full: r.Full, Model: r.Model, Wait: r.Wait}, err
}
