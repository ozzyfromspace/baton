package decide

import (
	"math"
	"strconv"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The weekly wind-down's words (state/winddown.go, loop/winddown.go): near the end of the account's weekly
// allowance, the run finishes what it started and starts nothing new, then goes on after the weekly reset.

// pctLeft is how much of the weekly allowance is left at a reading of used percent, as the human reads
// it: "2%", "1.4%".
func pctLeft(used float64) string {
	return strconv.FormatFloat(math.Max(0, math.Round((100-used)*10)/10), 'f', -1, 64) + "%"
}

// WindDown is the announcement of the run winding down at a weekly reading of used percent: nothing new
// starts, the phases under way (finishing) finish, and baton goes on at goesOn, after the weekly reset,
// unless the human pauses or writes; paused, only once /baton run gives the run back.
func WindDown(used float64, finishing []string, goesOn time.Time, paused bool, now time.Time) string {
	s := "baton: " + pctLeft(used) + " of weekly usage left: nothing new starts."
	switch len(finishing) {
	case 0:
	case 1:
		s += " " + finishing[0] + " finishes."
	default:
		s += " " + andList(finishing) + " finish."
	}
	if paused {
		return s + " The run is paused: once /baton run gives it back, baton goes on after the weekly reset, at " + Clock(goesOn, now) + "."
	}
	return s + " baton goes on at " + Clock(goesOn, now) + ", after the weekly reset, unless you pause or write."
}

// WindDownHolds is why a phase that could start waits in the wind-down, until goesOn (the board's ready
// line, a phase agent's hold).
func WindDownHolds(goesOn, now time.Time) string {
	return "nothing new starts in the weekly wind-down, until " + Clock(goesOn, now)
}

// WindDownDone is what baton done tells the lead's model when the wind-down leaves it no next phase: the
// run goes on at goesOn.
func WindDownDone(goesOn, now time.Time) string {
	return "The run is winding down near the end of the account's weekly usage: no new phase starts until the weekly reset, and baton goes on at " +
		Clock(goesOn, now) + ". End your turn now; baton wakes you then."
}

// WoundUp is the announcement of the run going on after its wind-down, by why (state.WindReset, …),
// taking up took ("": the lead holds a phase already, or none can start).
func WoundUp(by, took string) string {
	s := "baton: the weekly usage has reset, so the run goes on"
	switch by {
	case state.WindReading:
		s = "baton: the weekly usage reads below the reserve again, so the run goes on"
	case state.WindOff:
		s = "baton: weekly_reserve_pct is off, so the run goes on"
	}
	if took != "" {
		return s + ", with " + took + "."
	}
	return s + ": new phases start again."
}

// WindCancelled is the announcement that the wind-down will not go on by itself: the human paused baton, or
// wrote at the lead while it waited (state.CancelPrompt), which paused the run.
func WindCancelled(by string) string {
	why := "you paused baton"
	if by == state.CancelPrompt {
		why = "you wrote while the run wound down, so baton paused the run"
	}
	return "baton: going on after the weekly reset cancelled (" + why + "): baton will not go on by itself; /baton run resumes."
}

// WindCancelledTell is what the lead's model is told beside the human's prompt that paused the wind-down.
const WindCancelledTell = "[baton] The human wrote while the run wound down near the end of the account's weekly usage, so baton " +
	"paused the run and will not go on by itself after the weekly reset. Answer the human; the plan goes on when they run /baton run."

// StatusWindDown is what baton status says of a run wound down at a weekly reading of used percent, which
// goes on at goesOn, or, paused, once /baton run gives it back.
func StatusWindDown(used float64, goesOn time.Time, paused bool, now time.Time) string {
	s := "weekly wind-down: " + pctLeft(used) + " of weekly usage left → nothing new starts; what is under way finishes"
	if paused {
		return s + " · paused: /baton run gives it back, and baton goes on after the weekly reset, at " + Clock(goesOn, now)
	}
	return s + " · baton goes on at " + Clock(goesOn, now) + " unless you pause or write"
}

// WindRearmed is the announcement that a wound-down run the human gave back (/baton run) goes on by itself
// again, at goesOn.
func WindRearmed(goesOn, now time.Time) string {
	return "baton: the run still winds down: nothing new starts until the weekly reset. baton goes on at " + Clock(goesOn, now) +
		" unless you pause or write."
}
