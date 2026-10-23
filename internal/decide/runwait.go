package decide

import (
	"fmt"
	"strconv"
)

// RunWait is what the rest of the run does while a phase agent's block waits on the human, as the host saw
// it when it put the question (loop's runWait): Stuck when nothing else in the run can move
// (state.State.Moves), and Behind, the phases not done that wait on the blocked one, directly or not
// (plan.Plan.Descendants). The zero RunWait says the rest of the run goes on. A real run's block question
// said that nine times over five hours while every phase left waited on it.
type RunWait struct {
	Stuck  bool
	Behind []string
}

// behindNamed is how many of the phases behind a block its question names before it counts the rest.
const behindNamed = 8

// blockRest says what the rest of the run does while the block of the agent on phase waits on the human:
// it goes on, or nothing else in it can move until the block is cleared, and which phases wait on it.
func blockRest(phase string, w RunWait) string {
	const stuck = "Nothing else in the run can move until it is cleared"
	switch n := len(w.Behind); {
	case !w.Stuck:
		return "Only " + phase + " waits for you; the rest of the run goes on."
	case n == 0:
		return stuck + "."
	case n == 1:
		return fmt.Sprintf("%s: %s waits on %s.", stuck, w.Behind[0], phase)
	case n > behindNamed:
		named := append(w.Behind[:behindNamed:behindNamed], strconv.Itoa(n-behindNamed)+" more")
		return fmt.Sprintf("%s: %s wait on %s.", stuck, andList(named), phase)
	}
	return fmt.Sprintf("%s: %s wait on %s.", stuck, andList(w.Behind), phase)
}
