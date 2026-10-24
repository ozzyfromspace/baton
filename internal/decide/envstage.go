package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A stage the machine killed is the machine's (v0.8.17): in a real run the out-of-memory killer took a QA
// set's build in three of four whole runs of 40 to 49 minutes, and the agent fixed the cause only after the
// third, reading each as one more red stage. A red staged gate's result names each stage its environment
// took (lane.StageEnvironment, Log.StageEnv), the next whole run of its command is questioned once
// (GateEnvAgain, cli/envagain.go), and a second whole run of a phase's gate that loses a stage to the same
// kind is pushed to the human, once per phase and kind (EnvRepeated, loop/envrepeat.go).

// killed reports a loss to a process killed for memory: the machine's.
func killed(l state.EnvLoss) bool { return l.Kind == lane.EnvKilled }

// envStages is what red gate j's result says of the stages its environment took (Log.StageEnv): each, with
// its line, as the machine's or the environment's, not the code's, and how to go on: rerun it alone, and
// give it room or fix the environment before a whole run. "" when there are none.
func envStages(j state.Job, log Log) string {
	if j.Kind == state.JobAwait || !j.Red() || len(log.StageEnv) == 0 {
		return ""
	}
	var b strings.Builder
	var machine, other int
	for _, l := range log.StageEnv {
		if killed(l) {
			machine++
			fmt.Fprintf(&b, "`%s` was killed by the machine, not failed by the code: `%s`.\n", l.Stage, ticked(l.Line))
			continue
		}
		other++
		fmt.Fprintf(&b, "`%s` failed on its environment (%s), not on the code: `%s`.\n", l.Stage, l.Kind, ticked(l.Line))
	}
	if machine > 0 {
		b.WriteString(roomFor(machine) + "\n")
	}
	if other > 0 {
		it, they := plural(other, "it", "them"), plural(other, "it needs", "they need")
		fmt.Fprintf(&b, "Fix the environment, then rerun %s alone with `baton verify --failed`, and record what %s with `baton env`.\n", it, they)
	}
	return b.String() + "\n"
}

// roomFor is how to go on from n stages the machine killed: rerun them alone, and give them room before a
// whole run.
func roomFor(n int) string {
	const one = "Rerun it alone with `baton verify --failed`. A whole run puts it beside the heaviest stages again, so before one, give it room: " +
		"order the gate so it runs apart, or free memory, and record what it needs with `baton env`."
	if n == 1 {
		return one
	}
	return "Rerun them alone with `baton verify --failed`. A whole run puts them beside the heaviest stages again, so before one, give them room: " +
		"order the gate so they run apart, or free memory, and record what they need with `baton env`."
}

// ticked is line fit to quote between backticks: a backtick of its own becomes a quote.
func ticked(line string) string { return strings.ReplaceAll(line, "`", "'") }
