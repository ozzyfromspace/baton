package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says about a phase's jobs of each kind: a gate (`baton verify`) and a wait on something
// outside the project (`baton await`) run side by side, one of each at a time, and the phase stands on
// both (state.Standing). Within a kind, only the same check queued again replaces the one that runs;
// another is refused unless the model asks for it with --replace (state.Clash).

// JobNoun is what a job of kind is, for the model: "a gate" or "a wait on something outside".
func JobNoun(kind string) string {
	if kind == state.JobAwait {
		return "a wait on something outside"
	}
	return "a gate"
}

// Clash turns down a job of kind with command for phase while its job of that kind, running, runs another
// check: baton stops it only when asked (--replace), and says the other ways forward. Nothing waits on the
// human: the model picks one.
func Clash(kind, command string, running state.Job) string {
	return fmt.Sprintf("not queued — %s's job %s (%s), %s, has not ended, and a phase runs one %s at a time "+
		"(one gate beside one wait on something outside: `baton verify` and `baton await` never stop each other). "+
		"`%s` is not the same check, so baton does not stop %s unasked. Either:\n"+
		"- run it again with --replace: baton stops %s and runs this one instead;\n"+
		"- fold both checks into one command, and queue that with --replace; or\n"+
		"- queue this one once %s's result comes: baton gives it to you when it ends.",
		running.Phase, running.ID, Ran(running), status(running), kindName(kind), command, running.ID, running.ID, running.ID)
}

// kindName is a kind's name in "one … at a time": "gate", "wait on something outside".
func kindName(kind string) string {
	return strings.TrimPrefix(JobNoun(kind), "a ")
}

// status is how an unfinished job stands: queued, or running.
func status(j state.Job) string {
	if j.Status == state.JobRunning {
		return "running"
	}
	return "queued"
}

// Beside tells the model, as job j is queued, that it runs beside other, the phase's job of the other kind,
// and that the phase is done once both pass: "j66 runs beside j65 (`gh run watch 7`), a wait on something
// outside; …".
func Beside(j, other state.Job) string {
	return fmt.Sprintf("%s runs beside %s (`%s`), %s: neither stops the other, and `baton done %s` waits for both, and is refused while either is red.",
		j.ID, other.ID, other.Command, JobNoun(other.Kind), j.Phase)
}

// BesideShort is the same for the human's line: "it runs beside j65, an await".
func BesideShort(other state.Job) string {
	kind := "a gate"
	if other.Kind == state.JobAwait {
		kind = "an await"
	}
	return fmt.Sprintf("it runs beside %s, %s", other.ID, kind)
}

// Replaced tells the model that the job it queued replaces old, which baton stops: the same check queued
// again, or another one it asked to replace it with (--replace).
func Replaced(old state.Job, asked bool) string {
	if asked {
		return fmt.Sprintf("it replaces %s (`%s`), which baton stops, as --replace asked.", old.ID, old.Command)
	}
	return fmt.Sprintf("it replaces %s, the same check queued before, which baton stops.", old.ID)
}

// Jobs names jobs for the model: "job j4 (`make test`)", "jobs j4 (`make test`) and j5 (`gh run watch 7`)".
func Jobs(jobs []state.Job) string {
	var names []string
	for _, j := range jobs {
		names = append(names, fmt.Sprintf("%s (`%s`)", j.ID, j.Command))
	}
	if len(names) == 1 {
		return "job " + names[0]
	}
	return "jobs " + andList(names)
}

// VerifyingOn is the human's line as phase is finished while its jobs (one of each kind at most) run on:
// it is done once they pass, and reopens if one fails.
func VerifyingOn(phase string, jobs []state.Job) string {
	if len(jobs) == 1 {
		return fmt.Sprintf("baton: %s verifying — job %s (`%s`) runs on; %s is done once it passes, and reopens if it fails", phase, jobs[0].ID, jobs[0].Command, phase)
	}
	return fmt.Sprintf("baton: %s verifying — %s run on; %s is done once both pass, and reopens if either fails", phase, Jobs(jobs), phase)
}

// FinishedVerifying tells the model that phase is finished and verifying on jobs: baton marks it done
// once they pass, and takes it up again from its notes if one fails.
func FinishedVerifying(phase string, jobs []state.Job) string {
	if len(jobs) == 1 {
		return fmt.Sprintf("baton: %s is finished, and verifying: job %s (%s) is still running. baton marks %s done when it passes; "+
			"if it fails, %s reopens, and is taken up again from your notes on that job.", phase, jobs[0].ID, Ran(jobs[0]), phase, phase)
	}
	var names []string
	for _, j := range jobs {
		names = append(names, fmt.Sprintf("%s (%s)", j.ID, Ran(j)))
	}
	return fmt.Sprintf("baton: %s is finished, and verifying: jobs %s are still running. baton marks %s done when both pass; "+
		"if either fails, %s reopens, and is taken up again from your notes on that job.", phase, andList(names), phase, phase)
}
