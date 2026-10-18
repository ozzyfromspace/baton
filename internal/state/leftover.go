package state

import "slices"

// What a phase left running (package leftover): a phase agent's `baton done` is refused once while its
// worktree or a lane it used has something of its running (AskLeftovers), and a gate that runs past its
// history on a busy machine has its owner told what finished phases left running (a look-in, LookLeftover).

// LookLeftover is the look-in about what finished phases left running, as Job.LookedIn records it; one
// about no job (a worker retired with containers left) tells the lead.
const LookLeftover = "leftovers"

// AskedLeftovers reports whether phase's `baton done` was refused once over what it left running.
func (st State) AskedLeftovers(phase string) bool {
	return slices.Contains(st.Run.LeftoverAsked, phase)
}

// AskLeftovers records that phase's `baton done` is refused once over what it left running: the same call
// again goes through. It reports whether it was not recorded before.
func AskLeftovers(st *State, phase string) bool {
	if st.AskedLeftovers(phase) {
		return false
	}
	st.Run.LeftoverAsked = append(st.Run.LeftoverAsked, phase)
	return true
}
