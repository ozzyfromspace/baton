package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A session that waits on a job needs nothing from the human, but Claude Code's own idle notice, once the
// turn has ended, reads as if it did. The session lines baton gives at such a stop say so.

// GateWait is the session line when the phase in hand (cur) waits on its job j, and nothing else: no
// one needs to do anything.
func GateWait(cur string, j state.Job) string {
	return fmt.Sprintf("baton: %s waits on job %s (`%s`) — nothing is needed from you: Claude is given its result when it ends", cur, j.ID, j.Command)
}

// NoPhaseYet is the session line when no phase is in hand because every phase left waits on a job
// (waiting names them), or here, a gate that runs in the working tree, holds it: no one needs to do
// anything.
func NoPhaseYet(waiting string, here *state.Job) string {
	if here != nil {
		return fmt.Sprintf("baton: job %s (`%s`, %s) runs its gate in this working tree, so no phase starts here until it ends — "+
			"nothing is needed from you: baton takes the next one up then", here.ID, here.Command, here.Phase)
	}
	if waiting == "" {
		return "baton: no phase can start yet; baton takes the next one up as soon as one can"
	}
	return "baton: no phase can start until " + waiting + " finishes — nothing is needed from you: baton takes the next one up then"
}
