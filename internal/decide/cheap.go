package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What a gate's result says of the cheap path (v0.8.11): the tests its log says failed, by id
// (lane.FailureIDs), and the command that reruns only them, which stands in for the gate once it passes
// (`baton verify --failed -- <command>`); and, at its end, what the phase's gates have cost so far
// (GateCost), with what a long one cost the human, who feels the time the model does not (TimeShort).

// failedTests is what the result of red gate j says of the tests its log says failed (Log.Failed): their
// ids, and the rerun of only them where one can stand in for j (narrowRerun). "" for any other job, and a
// log that names none.
func failedTests(j state.Job, log Log) string {
	if j.Kind == state.JobAwait || !j.Red() || len(log.Failed) == 0 {
		return ""
	}
	return failedList(log) + narrowRerun(j, log) + "\n"
}

// failedList names the tests in Log.Failed: "Failed (3): TestA, TestB and internal/lane (failed whole).",
// at most maxIDs of them, with how many more; an id two of them share comes with where each ran.
func failedList(log Log) string {
	shown := log.Failed
	if len(shown) > maxIDs {
		shown = shown[:maxIDs]
	}
	seen := map[string]int{}
	for _, f := range shown {
		seen[f.ID]++
	}
	names := make([]string, 0, len(shown)+1)
	for _, f := range shown {
		name := f.ID
		switch {
		case f.Whole:
			name += " (failed whole)"
		case seen[f.ID] > 1 && f.Scope != "":
			name += " (" + f.Scope + ")"
		}
		names = append(names, name)
	}
	n := len(log.Failed) + log.FailedMore
	if rest := n - len(shown); rest > 0 {
		names = append(names, fmt.Sprintf("%d more", rest))
	}
	return fmt.Sprintf("Failed (%d): %s.\n", n, andList(names))
}

// narrowable reports whether a rerun of only the tests in Log.Failed can stand in for red gate j: j failed
// (it did not run out of time, which leaves what it never reached unchecked), recorded no stages (whose own
// --failed reruns what failed in it), and its log names every failure and no environment's.
func narrowable(j state.Job, log Log) bool {
	return j.Kind != state.JobAwait && j.Status == state.JobFailed && !staged(j) && log.Env == nil &&
		len(log.Failed) > 0 && log.FailedMore == 0
}

// narrowRerun offers red gate j's cheap path, when one can stand in for it (narrowable): the command that
// reruns only the tests its log says failed, in their runners' own form (lane.Reruns), queued as `baton
// verify --failed -- <command>`. Tests no runner of baton's named (a ::baton fail line's) are named by the
// gate's own means.
func narrowRerun(j state.Job, log Log) string {
	if !narrowable(j, log) {
		return ""
	}
	cmd := strings.Join(lane.Reruns(log.Failed), " && ")
	bare := false
	for _, f := range log.Failed {
		bare = bare || f.Runner == ""
	}
	const as = "as `baton verify --failed -- <that command>`, which clears this gate when it passes.\n"
	if bare || cmd == "" {
		s := "Rerun only these, by a command that names each of them"
		if cmd != "" {
			s += fmt.Sprintf(" (for those its test runner named: `%s`)", cmd)
		}
		return s + ", " + ownWay + ", " + as
	}
	how := ", " + ownWay
	if firstWord(cmd) == firstWord(j.Command) {
		how = "" // the gate runs the runner itself
	}
	return fmt.Sprintf("Rerun only these: `%s`%s, %s", cmd, how, as)
}

// ownWay is where a narrowed command runs: in whatever the gate's command sets up around its tests.
const ownWay = "inside the gate's own way of running it (make, docker compose)"

func firstWord(cmd string) string {
	if f := strings.Fields(cmd); len(f) > 0 {
		return f[0]
	}
	return ""
}

// narrowedFix is what the model does about red gate j once a rerun of only what failed can stand in for it
// (narrowRerun), and how that clears it.
func narrowedFix(j state.Job) string {
	return fmt.Sprintf("Fix what failed (quick checks in place help), commit, and rerun only what failed, as above: "+
		"a red gate stands until the same command passes, or a rerun of what failed in it that names every test above (`--failed`); "+
		"no other narrower check clears it (to make another command %s's gate instead, verify it with --replace: a decision without the human, and they are told). "+
		"`baton done %s` is refused until then.", j.Phase, j.Phase)
}

// spent is what ends gate j's result: what its phase has spent in gates so far (GateCost of c), and, for a
// gate that ran GateLong or more, what it cost the human (TimeShort). "" for an await, and for a gate with
// neither to say.
func spent(j state.Job, c state.Cost) string {
	if j.Kind == state.JobAwait {
		return ""
	}
	var parts []string
	if line := GateCost(j.Phase, c); line != "" {
		parts = append(parts, line)
	}
	if d := gateRan(j); d >= GateLong {
		parts = append(parts, TimeShort(d))
	}
	return strings.Join(parts, " ")
}

// gateRan is how long gate j ran: from when its command began (Ran, else Started) to its end.
func gateRan(j state.Job) time.Duration {
	began := j.Ran
	if began.IsZero() {
		began = j.Started
	}
	if began.IsZero() || j.Ended.Before(began) {
		return 0
	}
	return j.Ended.Sub(began)
}
