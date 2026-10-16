package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What the model is told when a job it handed to baton ends (baton verify, baton await), and how `done`
// treats a phase whose verification is still running or red. A result says what to do next, because the
// obvious reading is not always right: a lane error is not a test result, and a failure in the area of
// another red phase is that phase's.

// Span is a duration as a person reads it: 45s, 12m, 2h05m.
func Span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// Outcome is how a job ended, after its duration: "passed after 12m", "failed (exit 1) after 12m".
func Outcome(j state.Job) string {
	took := " after " + Span(j.Took(j.Ended))
	why := ""
	if j.Reason != "" {
		why = ": " + j.Reason
	}
	switch j.Status {
	case state.JobPassed:
		return "passed" + took
	case state.JobFailed:
		return fmt.Sprintf("failed (exit %d)%s", j.Exit, took)
	case state.JobTimeout:
		return "ran out of time" + took
	case state.JobLaneError:
		return "could not run" + why
	case state.JobInterrupted:
		return "was interrupted" + took + why
	}
	return j.Status
}

// Ran is what a job ran: "`make test` on 3f2a9c1b04de", and "in the working tree" for a gate run there
// (--here).
func Ran(j state.Job) string {
	what := "`" + j.Command + "`"
	if j.Commit != "" {
		what += " on " + short(j.Commit)
	}
	if j.Here {
		what += " in the working tree"
	}
	return what
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// A Log is what a result shows of a job's log (lane.ReadExcerpt): where it is, its end, and the lines
// before that end that report a failure, with how many more there are past those.
type Log struct {
	Path     string
	Tail     string
	Failures []string
	More     int
}

// JobResult is a finished job's result for the model: what ran and how it ended, the lines of its log that
// report a failure before its end, the end of its log, and what to do about it. others are the phases red
// now (state.Red); those of other phases are named, so the model leaves failures in their area to them,
// and the job's own phase's red gate is named when a gate of another command passed, since that does not
// clear it. qa is the QA phase ahead of the job's phase while the plan aggregates its heavy checks there
// (plan.QAAhead, aggregate_qa), "" otherwise: a red result then may be other phases' work in progress,
// which only the QA phase need make whole.
func JobResult(j state.Job, log Log, others []state.Job, qa string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[baton] Job %s (%s) %s: %s.\n", j.ID, j.Phase, Outcome(j), Ran(j))
	if len(log.Failures) > 0 && j.Status != state.JobPassed {
		fmt.Fprintf(&b, "Lines that report a failure earlier in its log, before its end below:\n\n%s\n", strings.Join(log.Failures, "\n"))
		if log.More > 0 {
			fmt.Fprintf(&b, "(and %d more such %s: search the log for them)\n", log.More, plural(log.More, "line", "lines"))
		}
		b.WriteString("\n")
	}
	if tail := strings.TrimSpace(log.Tail); tail != "" {
		fmt.Fprintf(&b, "The end of its log (%s):\n\n%s\n\n", log.Path, tail)
	} else if log.Path != "" {
		fmt.Fprintf(&b, "Its log: %s\n", log.Path)
	}
	switch {
	case j.Status == state.JobPassed && j.Kind == state.JobVerify:
		fmt.Fprintf(&b, "It checked %s: what you committed after it is not in this result.", short(j.Commit))
		if g := ownRedGate(others, j); g != nil {
			b.WriteString(" " + NotCleared(*g))
		}
	case j.Status == state.JobPassed:
		b.WriteString("Carry on.")
	case j.Status == state.JobLaneError && j.Here:
		fmt.Fprintf(&b, "This is not a test result: the gate's command never ran, so do not look for a bug in %s's code because of it. "+
			"Check the command, then verify again, or run the gate in place.", j.Phase)
	case j.Status == state.JobLaneError:
		fmt.Fprintf(&b, "This is not a test result: the gate never ran on its own terms, so do not look for a bug in %s's code because of it. "+
			"Run the gate in place instead, and `baton note` that you did. (baton status says how to set lanes up with a %s script.)", j.Phase, ".baton-lane")
	case Hung(j):
		b.WriteString("baton stopped it as hung, so nothing was checked: find what it waits on (a server that never answers, input, a lock) before you hand it to baton again, " +
			"or run it in place with a timeout of its own.")
	case j.Status == state.JobInterrupted:
		b.WriteString("Nothing was checked. Hand it to baton again, or run it in place.")
	case j.Kind == state.JobAwait:
		b.WriteString("What it waited on did not succeed: look into it, then await it again or carry on without it.")
	default:
		if j.Status == state.JobTimeout {
			b.WriteString("It ran past verify_timeout, so baton stopped it: that counts as a failure. ")
		}
		fmt.Fprintf(&b, "Fix what failed (quick checks in place help), commit, and verify again with the same command, `%s`: %s "+
			"`baton done %s` is refused until then.", j.Command, gateRule(j.Phase), j.Phase)
	}
	if j.Red() && qa != "" {
		b.WriteString(" " + othersMayBeWhy(j.Phase, qa))
	}
	if red := RedPhases(others, j.Phase); red != "" {
		b.WriteString("\n" + red)
	}
	return b.String()
}

// othersMayBeWhy is what a red result of phase, not a QA phase, adds while the plan leaves its heavy checks
// to the QA phase qa ("P10 (QA)"): the phases before qa run focused checks only and may leave work in
// progress on the branch, so the failure may be theirs. The phase fixes its own part, and finishes over
// the rest, which is a decision without the human, rather than waiting on what qa makes whole.
func othersMayBeWhy(phase, qa string) string {
	return fmt.Sprintf("This plan leaves its full checks to %s, and other phases' work in progress may be why this is red: "+
		"fix what is %s's, and if the rest is not, finish with `baton done %s --despite-red \"<why>\"` (the human is told) and leave it to %s.",
		qa, phase, phase, qa)
}

// RedPhases names the phases other than phase that are red now ("" for none): they share the branch with
// it, so failures in their area are theirs.
func RedPhases(red []state.Job, phase string) string {
	var names []string
	for _, j := range red {
		if j.Phase != phase {
			names = append(names, fmt.Sprintf("%s (job %s, %s %s)", j.Phase, j.ID, Ran(j), Outcome(j)))
		}
	}
	if len(names) == 0 {
		return ""
	}
	whose := "not this phase's"
	if phase == "" {
		whose = "not yours"
	}
	return "Red now: " + strings.Join(names, "; ") + ". Failures in their area are theirs, " + whose + ": leave them to " +
		plural(len(names), "it", "them") + "."
}

// hungMark begins the reason of a job baton stopped as hung (HungReason).
const hungMark = "hung — "

// HungReason is why baton stopped a gate as hung (Job.Reason): its log had not grown, and its processes
// had used no CPU, for quiet. Such a job has no verdict: its phase counts as unverified.
func HungReason(quiet time.Duration) string {
	return hungMark + "its log had not grown, and its processes had used no CPU, for " + Span(quiet)
}

// Hung reports a job baton stopped as hung (HungReason).
func Hung(j state.Job) bool {
	return j.Status == state.JobInterrupted && strings.HasPrefix(j.Reason, hungMark)
}

// JobHung tells the human that baton stops job j as hung, its log and its processes still for quiet.
func JobHung(j state.Job, quiet time.Duration) string {
	return fmt.Sprintf("baton: job %s (`%s`, %s) looks hung — its log has not grown, and its processes have used no CPU, for %s → baton stops it, and it counts as unverified",
		j.ID, j.Command, j.Phase, Span(quiet))
}

// FinishedAside tells the human that job j, as it ended, was the job of a phase set aside while it ran:
// nothing else says so until the phase comes back, which it does once the phase in hand is done or set
// aside in turn (state.Next). "" when j is not such a job, or its result needs no telling (it was
// cancelled, or given already).
func FinishedAside(st state.State, j state.Job) string {
	ps := st.Phases[j.Phase]
	if !j.Finished() || !j.Delivered.IsZero() || ps == nil || ps.Status != state.PhaseParked || ps.Park == nil || ps.Park.Job != j.ID {
		return ""
	}
	line := fmt.Sprintf("baton: job %s (`%s`, %s) %s", j.ID, j.Command, j.Phase, Outcome(j))
	if st.Mode == state.ModeRunning && st.Current != "" {
		line += fmt.Sprintf(" → %s comes back once %s is done or set aside", j.Phase, st.Current)
	}
	return line
}

// JobsFinished is the line baton types to wake an idle model when jobs it handed over have finished; the
// results come with it.
func JobsFinished(jobs []state.Job) string {
	return fmt.Sprintf("[baton] %s finished; %s attached.", jobsNamed(jobs), plural(len(jobs), "its result is", "their results are"))
}

// AgentJobsFinished is the message that wakes an idle phase agent for the results of jobs. Claude Code
// gives a resumed agent nothing of baton's hooks as it starts, so they come as it ends the turn, or with
// its next tool call.
func AgentJobsFinished(jobs []state.Job) string {
	return fmt.Sprintf("[baton] %s finished. %syou %s then%s", jobsNamed(jobs), endTurnFor, plural(len(jobs), "its result", "their results"), orToolCall)
}

// jobsNamed names jobs and their phases: "Job j4 (P3)", "Jobs j4, j5 (P3, P1)".
func jobsNamed(jobs []state.Job) string {
	ids, phases := make([]string, len(jobs)), map[string]bool{}
	var ph []string
	for i, j := range jobs {
		ids[i] = j.ID
		if !phases[j.Phase] {
			phases[j.Phase] = true
			ph = append(ph, j.Phase)
		}
	}
	return fmt.Sprintf("%s %s (%s)", plural(len(jobs), "Job", "Jobs"), strings.Join(ids, ", "), strings.Join(ph, ", "))
}

// PlanComplete is what the model does once the plan is complete; ds are the run's decisions (its phase
// agents' too): it names those made without the human, and the proposals answered Not now, which are
// left for them unless the model did them (and noted it).
func PlanComplete(ds []state.Decision) string {
	summary := "Summarize the results for the human"
	if n := len(WithoutHuman(ds)); n > 0 {
		summary += fmt.Sprintf(", including the %d %s this run made without them (baton status lists them, each with its undo)",
			n, plural(n, "decision", "decisions"))
	}
	left := LeftForYou(ds)
	if len(left) == 0 {
		return summary + ", and end your turn."
	}
	var names []string
	for _, d := range left {
		names = append(names, fmt.Sprintf("%s (%s)", d.ID, clause(d.What)))
	}
	return summary + fmt.Sprintf(". Name the %s answered Not now, which %s left for them unless it was done as safely reversible and noted: %s; "+
		"say which of them were done, and which wait for them. Then end your turn.",
		plural(len(left), "proposal", "proposals"), plural(len(left), "is", "are"), strings.Join(names, "; "))
}

// VerifiedComplete tells the model that the verification of the last phase passed, so the plan is
// complete; ds are the run's decisions (PlanComplete).
func VerifiedComplete(ds []state.Decision) string {
	return "[baton] That verification was the last thing the plan waited for: the plan is complete. " + PlanComplete(ds)
}

// RedRefusal turns down done for a phase that stands on a red job j (its gate, or its latest wait on
// something outside), and gives the ways forward. after are the checks of j's kind queued since, none of
// which cleared it (state.After): for a gate, those of another command, or of its own that checked nothing.
func RedRefusal(j state.Job, after []state.Job) string {
	despite := fmt.Sprintf("If the failure is not %s's to fix, finish it anyway with `baton done %s --despite-red \"<why>\"`: "+
		"that is a decision without the human, and they are told.", j.Phase, j.Phase)
	if j.Kind == state.JobAwait {
		return fmt.Sprintf("not done — %s's latest wait on something outside, job %s (%s), %s. "+
			"Fix what failed, commit, and wait on it again (`baton await`, the same command); `baton done %s` then waits for it. %s",
			j.Phase, j.ID, Ran(j), Outcome(j), j.Phase, despite)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "not done — %s's gate, job %s (%s), %s.", j.Phase, j.ID, Ran(j), Outcome(j))
	if len(after) > 0 {
		var since []string
		for _, o := range after {
			how := "runs now"
			if o.Finished() {
				how = Outcome(o)
			}
			since = append(since, fmt.Sprintf("job %s (%s) %s", o.ID, Ran(o), how))
		}
		fmt.Fprintf(&b, " Since then, %s; that does not clear it.", strings.Join(since, "; "))
	}
	rule := gateRule(j.Phase)
	fmt.Fprintf(&b, " %s%s Fix what failed, commit, and verify again with `%s`; `baton done %s` then waits for it. %s",
		strings.ToUpper(rule[:1]), rule[1:], j.Command, j.Phase, despite)
	return b.String()
}

// gateRule is how a red gate of phase is cleared.
func gateRule(phase string) string {
	return fmt.Sprintf("a red gate stands until the same command passes, and a narrower check that passes does not clear it "+
		"(to make another command %s's gate instead, verify it with --replace: a decision without the human, and they are told).", phase)
}

// ownRedGate is the red gate j's phase stands on, among red (state.Red), when it is not j: a gate of
// another command than j's, which j's passing did not clear. nil when there is none.
func ownRedGate(red []state.Job, j state.Job) *state.Job {
	for i := range red {
		if g := &red[i]; g.Phase == j.Phase && g.Kind != state.JobAwait && g.ID != j.ID {
			return g
		}
	}
	return nil
}

// NotCleared says that red gate g of its phase stands although a check of another command passed.
func NotCleared(g state.Job) string {
	return fmt.Sprintf("It does not clear %s's gate, job %s (`%s`), which is red: %s",
		g.Phase, g.ID, g.Command, gateRule(g.Phase))
}

// RedGateStands is what the model is told as it queues gate j for a phase whose gate, red, runs another
// command: j does not clear it, whatever its result.
func RedGateStands(red, j state.Job) string {
	return fmt.Sprintf("note — %s's gate, job %s (`%s`), is red, and %s runs another command, so it does not clear it, whatever its result: %s",
		red.Phase, red.ID, red.Command, j.ID, gateRule(red.Phase))
}

// GateReplaced is the decision, and its undo, of making gate j its phase's gate in place of red, a red gate
// of another command (--replace).
func GateReplaced(red, j state.Job) (what, undo string) {
	return fmt.Sprintf("made `%s` (job %s) %s's gate in place of job %s (`%s`), which is red", j.Command, j.ID, j.Phase, red.ID, red.Command),
		fmt.Sprintf("verify %s again with `%s` and fix what it finds: that is %s's gate again", j.Phase, red.Command, j.Phase)
}

// GateReplacedSay tells the model that gate j is its phase's gate now, in place of red; d is the decision
// recorded, "" for none (the run is paused: the human is at the wheel).
func GateReplacedSay(d string, red, j state.Job) string {
	now := fmt.Sprintf("`%s` is %s's gate now, in place of job %s (`%s`), which is red", j.Command, j.Phase, red.ID, red.Command)
	if d == "" {
		return now + "."
	}
	return fmt.Sprintf("noted (%s): %s; the human will be told.", d, now)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
