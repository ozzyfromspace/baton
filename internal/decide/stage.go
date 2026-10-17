package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// What `baton stage` prints around a stage of a gate, into the job's log: where each stage starts and how
// it ended, read by the human watching the log and by the model in a result's tail.

// StageStart is the line a stage starts with: "▶ unit 14:02:11". A rerun's stage narrowed to what failed
// in the red gate says so, naming the first few of them.
func StageStart(name string, at time.Time, narrowed []string, from string) string {
	line := "▶ " + name + " " + at.Format("15:04:05")
	if len(narrowed) == 0 {
		return line
	}
	return fmt.Sprintf("%s — only the %d that failed in %s: %s", line, len(narrowed), gateName(from), listSome(narrowed, 5))
}

// StageEnd is the line a stage that ran ends with: "■ unit passed in 2m14s", "■ e2e failed (exit 1) in 12s".
func StageEnd(name string, exit int, took time.Duration) string {
	if exit == 0 {
		return "■ " + name + " passed in " + Stopwatch(took)
	}
	return fmt.Sprintf("■ %s failed (exit %d) in %s", name, exit, Stopwatch(took))
}

// StageCarried is the line of a rerun's stage that passed in the red gate the rerun stands in for, and is
// not run again: "■ lint passed in j80 on 72b7740".
func StageCarried(name, from, commit string) string {
	line := "■ " + name + " passed in " + gateName(from)
	if commit != "" {
		line += " on " + short7(commit)
	}
	return line + ", so this rerun does not run it again"
}

// StageHalted is the line of a stage that does not run because an earlier stage of the gate failed: a gate
// stops at its first failure, whatever its script does after it.
func StageHalted(name string, failed lane.Stage) string {
	return fmt.Sprintf("■ %s skipped: stage %s failed (exit %d) earlier in this gate, and a gate stops at its first failure "+
		"(a stage run with baton stage --keep-going lets the stages after it run)", name, failed.Name, failed.Exit)
}

// StageNoRun is the line of a stage whose command could not start.
func StageNoRun(name string, err error) string {
	return fmt.Sprintf("baton: stage %s could not run its command: %v", name, err)
}

// Stopwatch is a duration to the second: 14s, 2m14s, 1h02m.
func Stopwatch(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// gateName is how a stage's line names the red gate a rerun stands in for: by its job, when known.
func gateName(job string) string {
	if job == "" {
		return "the gate this reruns"
	}
	return job
}

// listSome lists the first n of items, and how many more there are.
func listSome(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}
