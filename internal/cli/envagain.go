package cli

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole run after one that lost a stage to its environment (v0.8.17, decide/envstage.go): in a real run
// the out-of-memory killer took a QA set's build in three of four whole runs, each run putting it beside the
// heaviest stages again. So a whole run of a gate whose last whole run lost a stage that way is questioned
// once per command and commit, as a gate out of proportion is (questionGate), with the stage, its line and
// the ways out: rerun it alone, or give it room first. --why answers it, the reason told to the human, and a
// long repeat queued again without one is flagged, as for every question of questionGate (why.go). A
// --failed rerun is never asked: it is the way out.

// askEnvAgain puts baton's question to a whole run of command, through add (questionGate's), when the
// command's last whole run in st, the run's state read before its lock, lost stages to its environment
// (state.Store.EnvLost: it reads the run's log, so it runs outside the lock). The run is a's job, and its
// time a's, when no question before it named one.
func (a *gateAsk) askEnvAgain(s *state.Store, st state.State, command string, add func(kind, question string)) {
	last := st.LastWhole(command)
	if last == nil {
		return
	}
	lost := s.EnvLost(*last)
	if len(lost) == 0 {
		return
	}
	if a.job == "" {
		a.job, a.took = last.ID, ranFor(*last)
	}
	add(decide.GateEnvAgain, decide.EnvAgain(*last, lost))
}
