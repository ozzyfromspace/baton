package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says of reruns and stops. A red gate that recorded its stages (the gate protocol, `baton
// stage`) is cleared by `baton verify --failed`: the same command again on what failed in it alone, with
// the stages that passed carried (state.Rerun). `baton stop` ends a job of the model's own, as at a gate's
// first failure, and leaves the gate it stood on as it was.

// maxIDs is how many of a narrowed stage's failures the words name.
const maxIDs = 20

// staged reports a gate that recorded its stages, so that --failed can rerun what failed in it alone.
func staged(j state.Job) bool { return j.Kind != state.JobAwait && len(j.Stages) > 0 }

// failedRule is what gateRule adds for red gate g once it recorded its stages: how --failed clears it.
func failedRule(g state.Job) string {
	if !staged(g) {
		return ""
	}
	return fmt.Sprintf(" As it recorded its stages, `baton verify --failed` runs `%s` again on what failed alone, with the stages that passed carried, "+
		"and clears it if that passes: the whole gate need not run again.", g.Command)
}

// again is how the model clears red gate j: verify again with its command ("with the same command" for
// same), or, once it recorded its stages, rerun what failed in it.
func again(j state.Job, same bool) string {
	switch {
	case staged(j):
		return "rerun what failed with `baton verify --failed --notes \"…\"`"
	case same:
		return fmt.Sprintf("verify again with the same command, `%s`", j.Command)
	}
	return fmt.Sprintf("verify again with `%s`", j.Command)
}

// RerunQueued tells the model, as it queues a rerun for phase, what the rerun carries, narrows and runs
// whole (state.PlannedRerun).
func RerunQueued(phase string, v state.RerunView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "it reruns what failed in job %s:\n", v.Of)
	if len(v.Carried) > 0 {
		fmt.Fprintf(&b, "- carried, not run again: %s\n", carried(v))
	}
	if len(v.Narrowed) > 0 {
		fmt.Fprintf(&b, "- narrowed to what failed there: %s\n", narrowed(v.Narrowed))
	}
	if len(v.Whole) > 0 {
		fmt.Fprintf(&b, "- run whole: %s\n", stageNames(v.Whole))
	}
	fmt.Fprintf(&b, "A stage %s's record does not name runs whole too. If it passes, it clears %s; if it fails, it is %s's red gate from then on, "+
		"and `baton verify --failed` reruns what failed in it. To run a carried stage again, add --stages <name>,<name>.", v.Of, v.Of, phase)
	return b.String()
}

// rerunRan is what the result of rerun j says it carried and narrowed (state.RanRerun), "" for a job that
// reran nothing.
func rerunRan(j state.Job, v *state.RerunView) string {
	if v == nil || j.Reruns == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "It reran what failed in job %s (verify --failed):\n", v.Of)
	if len(v.Carried) > 0 {
		fmt.Fprintf(&b, "- carried, not run again: %s\n", carried(*v))
	}
	if len(v.Narrowed) > 0 {
		fmt.Fprintf(&b, "- narrowed to what failed there: %s\n", narrowed(v.Narrowed))
	}
	if len(v.Carried) == 0 && len(v.Narrowed) == 0 {
		b.WriteString("- it carried nothing and narrowed nothing: every stage ran whole\n")
	}
	if j.Status == state.JobPassed {
		fmt.Fprintf(&b, "As it passed, it clears %s.", v.Of)
		for _, c := range v.Carried {
			if len(c.Changed) > 0 {
				b.WriteString(" The stages it carried passed on the commits named: if a file changed since is theirs, check it in place.")
				break
			}
		}
		b.WriteString("\n")
	}
	return b.String() + "\n"
}

// carried names v's carried stages, each with where it passed and the files changed since: "`unit`,
// passed in j80 on 72b7740c1d2e (changed since: a.go and b.go)".
func carried(v state.RerunView) string {
	var out []string
	for _, c := range v.Carried {
		s := "`" + c.Name + "`, passed"
		if c.From != "" {
			s += " in " + c.From
		}
		if c.Commit != "" {
			s += " on " + short(c.Commit)
		}
		switch {
		case c.Commit != "" && c.Commit == v.Commit:
			s += " (this commit)"
		case len(c.Changed) > 0:
			files := c.Changed
			if c.More > 0 {
				files = append(files[:len(files):len(files)], fmt.Sprintf("%d more", c.More))
			}
			s += " (changed since: " + andList(files) + ")"
		case c.Known:
			s += " (no file changed since)"
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

// narrowed names stages each with the failures it runs narrowed to: "`e2e` (TestA, TestB)".
func narrowed(stages []lane.Stage) string {
	var out []string
	for _, s := range stages {
		ids := s.Failures
		more := ""
		if len(ids) > maxIDs {
			ids, more = ids[:maxIDs], fmt.Sprintf(" and %d more", len(s.Failures)-maxIDs)
		}
		out = append(out, fmt.Sprintf("`%s` (%s%s)", s.Name, strings.Join(ids, ", "), more))
	}
	return strings.Join(out, "; ")
}

// stageNames is "`a`, `b`".
func stageNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "`" + n + "`"
	}
	return strings.Join(q, ", ")
}

// NoRedGate turns down --failed for phase, which has no red gate, nor a gate stopped after a failure.
// running is its gate queued or running now, if any.
func NoRedGate(phase string, running *state.Job) string {
	s := fmt.Sprintf("not queued — --failed reruns what failed in %s's red gate, and %s has none: no gate of %s went red since its last pass, "+
		"or was stopped after it showed a failure (`baton stop`).", phase, phase, phase)
	if running != nil {
		return s + fmt.Sprintf(" Its gate, job %s (%s), is %s and has shown no failure: baton gives you its result when it ends.", running.ID, Ran(*running), status(*running))
	}
	return s + " To check the phase, queue its gate itself: `baton verify --notes \"…\" -- <command>`."
}

// NoStages turns down --failed for the gate of phase it would rerun, of, which recorded no stages.
func NoStages(phase string, of state.Job) string {
	return fmt.Sprintf("not queued — job %s (%s), the gate --failed would rerun for %s, recorded no stages, so baton has no record of what passed in it "+
		"and what failed, and --failed reruns only from that record. Fix what failed, then verify again with `%s`. "+
		"For the gates to come, build the gate from stages: a committed script whose steps each run as `baton stage <name> -- <command>` "+
		"(a failed stage stops the ones after it, and --failed then reruns only what failed).", of.ID, Ran(of), phase, of.Command)
}

// UnknownStages turns down --stages naming names, which job of did not record among stages.
func UnknownStages(of state.Job, names []string, stages []lane.Stage) string {
	var have []string
	for _, s := range stages {
		have = append(have, s.Name)
	}
	return fmt.Sprintf("not queued — --stages names %s, which job %s did not record: its stages are %s.", stageNames(names), of.ID, stageNames(have))
}

// FailedCommand turns down --failed given command, which is not the command of of, the gate it reruns.
func FailedCommand(of state.Job, command string) string {
	return fmt.Sprintf("not queued — --failed reruns job %s's own command, `%s`, and `%s` is another. Leave the command out to rerun what failed in %s; "+
		"to make `%s` %s's gate instead, verify it with --replace.", of.ID, of.Command, command, of.ID, command, of.Phase)
}

// RerunChanged turns down --failed when phase's jobs changed between baton's look and its lock.
func RerunChanged(phase string) string {
	return fmt.Sprintf("not queued — %s's jobs changed as baton looked: run it again.", phase)
}

// StopReason is why a job stopped with `baton stop` ended (Job.Reason), with the model's why.
func StopReason(why string) string {
	if why = clause(why); why != "" {
		return "stopped with baton stop (" + why + ")"
	}
	return "stopped with baton stop"
}

// StopUnknown turns down `baton stop` of id, which the run has no job of; running are its jobs queued or
// running.
func StopUnknown(id string, running []state.Job) string {
	s := fmt.Sprintf("not stopped — this run has no job %s (a phase agent's jobs are its own to stop).", id)
	if len(running) == 0 {
		return s + " None of its jobs is queued or running."
	}
	var names []string
	for _, j := range running {
		names = append(names, fmt.Sprintf("%s (`%s`, %s)", j.ID, j.Command, j.Phase))
	}
	return s + " Its jobs queued or running: " + strings.Join(names, ", ") + "."
}

// StopEnded turns down `baton stop` of job j, which has ended.
func StopEnded(j state.Job) string {
	s := fmt.Sprintf("not stopped — job %s (`%s`, %s) has ended: it %s.", j.ID, j.Command, j.Phase, Outcome(j))
	if j.Red() && staged(j) {
		s += " To rerun only what failed, fix it, commit, and `baton verify --failed --notes \"…\"`."
	}
	return s
}

// Stopped tells the model that it stopped job j: its runner ends it within tick. red is the red gate its
// phase still stands on, if any; rerun says --failed has something to rerun now; waits says j's phase was
// verifying on it, so it reopens.
func Stopped(j state.Job, tick time.Duration, red *state.Job, rerun, waits bool) string {
	var b strings.Builder
	if j.Status == state.JobQueued {
		fmt.Fprintf(&b, "stopped job %s (%s, %s) before it started: it counts as interrupted, so it checked nothing.", j.ID, Ran(j), j.Phase)
	} else {
		fmt.Fprintf(&b, "stopped job %s (%s, %s): it counts as interrupted, so it checked nothing, and its runner ends what it runs, and brings its lane down, within %s.",
			j.ID, Ran(j), j.Phase, Span(tick))
	}
	if red != nil && red.ID != j.ID {
		fmt.Fprintf(&b, " %s's gate is still job %s (`%s`), red, as it was.", j.Phase, red.ID, red.Command)
	}
	if waits {
		fmt.Fprintf(&b, " %s waited on it after `baton done`, so it reopens as unverified.", j.Phase)
	}
	if rerun {
		b.WriteString(" To rerun only what failed, fix it, commit, and `baton verify --failed --notes \"…\"`: the stages that passed are carried.")
	}
	return b.String()
}

// StoppedLine tells the human that the model stopped job j, for why.
func StoppedLine(j state.Job, why string) string {
	return fmt.Sprintf("baton: job %s (`%s`, %s) %s → its runner ends it", j.ID, j.Command, j.Phase, StopReason(why))
}
