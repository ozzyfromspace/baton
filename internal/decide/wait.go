package decide

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A session that waits on a job needs nothing from the human, but Claude Code's own idle notice, once the
// turn has ended, reads as if it did. The session lines baton gives at such a stop say so.

// GateWait is the session line when the phase in hand (cur) waits on its jobs (a gate, a wait on something
// outside, or both), and nothing else: no one needs to do anything.
func GateWait(cur string, jobs []state.Job) string {
	if len(jobs) == 1 {
		j := jobs[0]
		return fmt.Sprintf("baton: %s waits on job %s (`%s`) — nothing is needed from you: Claude is given its result when it ends", cur, j.ID, j.Command)
	}
	var names []string
	for _, j := range jobs {
		names = append(names, fmt.Sprintf("%s (`%s`)", j.ID, j.Command))
	}
	return fmt.Sprintf("baton: %s waits on jobs %s — nothing is needed from you: Claude is given each result when it ends", cur, andList(names))
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

// RunStalled is the notice pushed to the human when nothing in the run has moved for quiet (loop's stall
// alarm): what each phase left waits on (waits, as the board says it: "P3: agent between turns 41m"), and
// where the lead stands (its line on the board).
func RunStalled(quiet time.Duration, waits []string, lead string) string {
	return fmt.Sprintf("the run has not moved for %s — %s. /baton board shows where it stands", Span(quiet), strings.Join(append(slices.Clip(waits), lead), "; "))
}

// LaneWaits is the session line when job j waits for one of the project's lanes, all slots of them being
// in use by gates (in lanes, and in phase agents' worktrees), with ahead jobs that began to wait before it
// waiting still: nothing is needed from anyone.
func LaneWaits(j state.Job, slots, ahead int) string {
	in := "the project's one lane is in use"
	if slots > 1 {
		in = fmt.Sprintf("all %d of the project's lanes are in use", slots)
	}
	switch {
	case ahead == 1:
		in += ", and 1 gate waits ahead of it"
	case ahead > 1:
		in += fmt.Sprintf(", and %d gates wait ahead of it", ahead)
	}
	return fmt.Sprintf("baton: job %s (`%s`, %s) waits for a lane: %s. It starts once one is free", j.ID, j.Command, j.Phase, in)
}

// LogQuiet says that a running job's log has not grown for d: its gate may hang.
func LogQuiet(d time.Duration) string { return "its log has not grown for " + Span(d) }
