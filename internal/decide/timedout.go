package decide

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a red gate's result says when its failures waited out a timeout (v0.8.15, lane.TimedOut): several
// ran for about the same long time, so they did not fail on what they checked but sat until a timeout ended
// them, and every red run of the gate pays that wait again. A failing test should fail in seconds.

// GateRan is how long gate j's command ran, from when it began (Ran, else Started) to its end: what the
// time of its failures that waited out a timeout is a share of (lane.TimedOut).
func GateRan(j state.Job) time.Duration { return gateRan(j) }

// timedOut is what the result of red gate j says of its failures that waited out a timeout (Log.TimedOut):
// "12 of 14 failures ran about 3m each, 36 of this gate's 40 minutes: they waited out a timeout. A failing
// test should fail in seconds; lower the timeout (…) so a red costs seconds, not minutes." "" for any other
// job, and a gate whose failures did not.
func timedOut(j state.Job, log Log) string {
	if j.Kind == state.JobAwait || !j.Red() || len(log.TimedOut) == 0 {
		return ""
	}
	n, of := len(log.TimedOut), len(log.Failed)+log.FailedMore
	which := fmt.Sprintf("%d of %d failures", n, of)
	if n >= of {
		which = fmt.Sprintf("All %d failures", n)
	}
	each, all := lane.Times(log.TimedOut)
	return fmt.Sprintf("%s ran about %s each, %s: they waited out a timeout. "+
		"A failing test should fail in seconds; lower the timeout (%s) so a red costs seconds, not minutes.\n\n",
		which, about(each), shareOf(all, gateRan(j)), timeoutsOf(log.TimedOut))
}

// about is d as the time each of a cluster of failures took: in seconds under 2 minutes ("90s"), else in
// whole minutes ("3m"), rounded.
func about(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(math.Round(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(math.Round(d.Minutes())))
	}
	return Span(d.Round(time.Minute))
}

// shareOf is the time failures took, all of them, against ran, the gate's: "36 of this gate's 40 minutes";
// "36 minutes in all, side by side in this gate's 15" when they ran at once and took more than the gate.
// In seconds for a gate that ran under 2 minutes.
func shareOf(all, ran time.Duration) string {
	unit, part, whole := "minutes", int(math.Round(all.Minutes())), int(math.Round(ran.Minutes()))
	if ran < 2*time.Minute {
		unit, part, whole = "seconds", int(math.Round(all.Seconds())), int(math.Round(ran.Seconds()))
	}
	if all > ran {
		return fmt.Sprintf("%d %s in all, side by side in this gate's %d", part, unit, whole)
	}
	return fmt.Sprintf("%d of this gate's %d %s", part, whole, unit)
}

// timeoutsOf names the timeouts to lower for the runners of fs, in the order they come: "Playwright
// `timeout` and `expect.timeout`, a go test's deadline".
func timeoutsOf(fs []lane.FailedTest) string {
	var names []string
	for _, f := range fs {
		name, ok := runnerTimeouts[f.Runner]
		if !ok {
			name = "the test's own timeout"
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

// runnerTimeouts are the timeouts a test of each runner that gives its tests' times waits out.
var runnerTimeouts = map[string]string{
	lane.RunPlaywright: "Playwright `timeout` and `expect.timeout`",
	lane.RunGo:         "a go test's deadline",
	lane.RunVitest:     "vitest `testTimeout`",
}
