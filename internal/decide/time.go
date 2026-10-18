package decide

import "time"

// What baton says, wherever a gate is decided, about the time a check takes: a model does not feel it, and
// the human does. Asking alone did not change what models ran (a real run since v0.8.7 resubmitted every
// gate baton questioned, unchanged, within seconds): the cost was abstract. These words make it concrete,
// with one real case, at plan approval (GateRules), in every brief (brief.qaGuide), and beside a gate's
// result or refusal (TimeShort).

// TimeFelt is the frame for every decision about a gate: the model does not feel a gate's minutes, the
// human feels each one, and a check that cannot change what ships harms them. It opens GateRules and
// comes before the QA line and the gate habits in every brief.
const TimeFelt = "You do not feel time pass. A gate that runs 12 minutes is one tool call to you, and its result arrives as if no time went by. " +
	"The human feels every one of those minutes: they wait on the run, their usage and money drain, and the work they care about next sits behind it. " +
	"A check that cannot change what ships is not caution: it harms them. " +
	"A real run started one 12-minute gate 11 times in 75 minutes, each time after fixing a single failure, " +
	"and spent 8 hours on three full passes of a 2h40m QA set that mostly re-proved what had passed. " +
	"Before any check, ask what it can tell you that you do not already know, and run only that."

// TimeShort is TimeFelt in one line, for a gate that ran or would run for d: "This gate spends 12m of the
// human's time; to you it is one tool call."
func TimeShort(d time.Duration) string {
	return "This gate spends " + Span(d) + " of the human's time; to you it is one tool call."
}
