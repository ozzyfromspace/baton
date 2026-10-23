package state

import "slices"

// A file in a second landing conflict of the run that phases not started name in their sections cues a
// replan of baton's own (ReplanConflict) at the lead's next phase boundary, once a file (package replan's
// Due): one file every phase edits serializes the phases. The merge queue records the cue as it leaves the
// lead its look-in (loop/lookin.go), since only it reads the plan's text then; the boundary settles it
// (State.Cue with the file), whether a replan opens or not.

// CueConflict records that each of paths, in its second landing conflict of the run, is named by the
// phases not started naming has for it (ConflictCue): each cues a replan once. A path cued before, or
// that no phase names, is passed over.
func CueConflict(st *State, paths []string, naming map[string][]string) {
	for _, p := range paths {
		ids := naming[p]
		if len(ids) == 0 || st.Cued(ReplanConflict, p) {
			continue
		}
		if st.ConflictCue == nil {
			st.ConflictCue = map[string][]string{}
		}
		st.ConflictCue[p] = slices.Clone(ids)
	}
}
