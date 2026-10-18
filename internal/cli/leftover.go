package cli

import (
	"context"
	"slices"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a phase starts, it stops (v0.8.7): a phase agent's `baton done` is refused once while its worktree,
// or a lane its gates ran in, has something of its running (package leftover): a process started under
// baton, or a container compose started there. The refusal lists each with the command that stops it
// (decide.LeftoverAsk), and the same call again goes through (Run.LeftoverAsked): something may have to
// stay up, and the model decides. The lead's own phases work in the checkout, where what runs may be the
// human's, and are not asked.

// askLeftovers refuses phase's `baton done` in s, a phase agent's sub-run whose state was st before its
// lock, once, while the phase leaves something running; it reports whether it did, and the exit code. The
// machine is read before the run's lock.
func askLeftovers(io IO, s *state.Store, st state.State, phase string) (int, bool) {
	if s.Worker() == "" || st.AskedLeftovers(phase) {
		return 0, false
	}
	lanes := phaseLanes(io, s, st, phase)
	skip := leftover.Here()
	for _, j := range st.Unfinished() {
		skip.Job(j.PID, j.PGID) // a gate --here, or a wait on something outside, runs in the worktree
	}
	left := io.Leftovers.Find(context.Background(), append([]string{s.Root}, lanes...), skip)
	if left.Empty() {
		return 0, false
	}
	s.Update(func(st *state.State) error {
		state.AskLeftovers(st, phase)
		return nil
	})
	ev := map[string]any{"phase": phase, "processes": leftover.PIDs(append(slices.Clone(left.Procs), left.Hidden...))}
	var names []string
	for _, c := range left.Containers {
		names = append(names, c.Name)
	}
	if len(names) > 0 {
		ev["containers"] = names
	}
	if len(lanes) > 0 {
		ev["lanes"] = lanes
	}
	s.Event("leftover_asked", ev)
	return fail(io, "%s", decide.LeftoverAsk(phase, s.Root, left)), true
}

// phaseLanes are the lanes the phase's gates ran in, of st, that nothing holds now: no job runs in them,
// and no host brought them up (what a lane's `.baton-lane up` started is the lane's, until its down).
func phaseLanes(io IO, s *state.Store, st state.State, phase string) []string {
	ran := map[int]bool{}
	for _, j := range st.Jobs {
		if j.Phase == phase && j.Lane > 0 && !j.Here && j.Finished() {
			ran[j.Lane] = true
		}
	}
	if len(ran) == 0 {
		return nil
	}
	var out []string
	for _, l := range lanePool(io, s).Lanes() {
		if ran[l.N] && !l.Busy && l.UpBy == "" {
			out = append(out, l.Dir)
		}
	}
	return out
}
