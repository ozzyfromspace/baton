package cli

import "github.com/ozzyfromspace/baton/internal/state"

// hereLandings is what a gate run in the lead's checkout (verify --here) holds as it runs, as the merge
// queue reads it (loop's Coordinator.gate): the phases of run st, the session's, whose work waits to land
// (state.WorkerLanding), and how many phases of the other live runs in the checkout do. Nothing for a
// worker's session, whose worktree no landing touches. The other runs are read through the project as it
// stands (state.Store.ProjectOf), with no lock.
func hereLandings(s *state.Store, st state.State) (landing []string, others int) {
	if s.Worker() != "" {
		return nil, 0
	}
	landing = landingPhases(st)
	for _, r := range s.ProjectOf().Others(s.Ref) {
		others += len(landingPhases(r.State))
	}
	return landing, others
}

// landingPhases are the phases of run st whose work waits in the merge queue, in roster order.
func landingPhases(st state.State) []string {
	var out []string
	for _, w := range st.Workers {
		if w.Status == state.WorkerLanding {
			out = append(out, w.Phase)
		}
	}
	return out
}
