package decide

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Look-ins tell the session what a gate or a landing of its does while it runs (state.LookIn): the first
// failure a gate prints, a gate past its history or near verify_timeout, landings held behind a gate in
// the checkout, a file that keeps conflicting. Each is information: it says what baton saw and what the
// model can do about it, and asks nothing. baton never stops a gate for what it printed; the model decides.

// lookClip is the most of a log's line a look-in quotes.
const lookClip = 300

// LookSlowFactor and LookTimeoutShare are where a running gate becomes a look-in: past LookSlowFactor
// times the slowest earlier passing run of its command (and loop's Timing.SlowAfter more), or at
// LookTimeoutShare of verify_timeout.
const (
	LookSlowFactor   = 1.5
	LookTimeoutShare = 0.8
)

// FirstFailure is the look-in that tells the model that its job j printed a failure f, in after its command
// began, and runs on. landing and others are what a gate in the lead's checkout holds as it runs (the
// phases of the run whose work waits to land, and how many of other baton sessions' do: landingsHeld);
// nil and 0 for any other.
func FirstFailure(j state.Job, f state.Failing, in time.Duration, landing []string, others int) string {
	var b strings.Builder
	where := ""
	if f.Stage != "" {
		where = ", in its stage " + f.Stage
	}
	if j.Kind == state.JobAwait {
		fmt.Fprintf(&b, "[baton] Job %s (%s), waiting on `%s`, printed a failure %s in%s:\n\n%s\n\n", j.ID, j.Phase, j.Command, Span(in), where, clipLine(f.Line))
		fmt.Fprintf(&b, "It waits on. To act on it now, stop it (`baton stop %s`); or let it finish, and baton gives you its result then.", j.ID)
		return b.String()
	}
	fmt.Fprintf(&b, "[baton] Job %s (%s) is failing as it runs: `%s` printed this %s in%s:\n\n%s\n\n", j.ID, j.Phase, j.Command, Span(in), where, clipLine(f.Line))
	fmt.Fprintf(&b, "The gate runs on, and baton leaves it to you. To act on it now, stop it (`baton stop %s`) and rerun what failed "+
		"(`baton verify --failed`), once you have a fix; or let it finish to collect every failure, and baton gives you its result then.", j.ID)
	if j.Here && (len(landing) > 0 || others > 0) {
		b.WriteString(" While it runs in the working tree, it holds landings: " + landingsHeld(landing, others))
	}
	return b.String()
}

// FirstFailureSaid tells the human that the model was told job j printed failure f, in after it began.
func FirstFailureSaid(j state.Job, f state.Failing, in time.Duration) string {
	return fmt.Sprintf("baton: job %s (`%s`, %s) printed a failure %s in, and runs on → told Claude", j.ID, j.Command, j.Phase, Span(in))
}

// GateSlow is the look-in for gate j, which has run ran: past LookSlowFactor times the slowest of its command's
// earlier passing runs (earlier, each how long it took, oldest first). still is how long its log (at log)
// has not grown, negative when not known; timeout is verify_timeout.
func GateSlow(j state.Job, ran time.Duration, earlier []time.Duration, still time.Duration, log string, timeout time.Duration) string {
	var spans []string
	for _, d := range earlier {
		spans = append(spans, Span(d))
	}
	return fmt.Sprintf("[baton] Job %s (%s, `%s`) has run %s, well past its slowest earlier pass: earlier passing runs of `%s` took %s. %s "+
		"It may hang, or do more than it did before: look at its log. If it is stuck, or what is left is not worth the wait, stop it (`baton stop %s`) "+
		"and run a narrower check; otherwise let it run, and baton gives you its result when it ends.%s",
		j.ID, j.Phase, j.Command, Span(ran), j.Command, andList(spans), logStill(still, log), j.ID, stopsAt(timeout))
}

// GateSlowSaid is the line that tells the human about gate j, past its history.
func GateSlowSaid(j state.Job, ran, slowest time.Duration) string {
	return fmt.Sprintf("baton: job %s (`%s`, %s) has run %s, past its slowest earlier pass (%s)", j.ID, j.Command, j.Phase, Span(ran), Span(slowest))
}

// GateNearTimeout is the look-in for gate j, which has run ran of the timeout it may run (verify_timeout).
// still is how long its log (at log) has not grown, negative when not known.
func GateNearTimeout(j state.Job, ran, timeout, still time.Duration, log string) string {
	return fmt.Sprintf("[baton] Job %s (%s, `%s`) has run %s of the %s a job may run (verify_timeout): at %s baton stops it, and that counts as a failure "+
		"though it found none. %s If it cannot finish in time, stop it now (`baton stop %s`) and check what is left with a narrower command.",
		j.ID, j.Phase, j.Command, Span(ran), Span(timeout), Span(timeout), logStill(still, log), j.ID)
}

// GateNearTimeoutSaid is the line that tells the human about gate j, near verify_timeout.
func GateNearTimeoutSaid(j state.Job, ran, timeout time.Duration) string {
	return fmt.Sprintf("baton: job %s (`%s`, %s) has run %s of the %s it may run", j.ID, j.Command, j.Phase, Span(ran), Span(timeout))
}

// HeldLanding is a phase whose work waits to land behind a gate in the lead's checkout, since when.
type HeldLanding struct {
	Phase string
	Since time.Time
}

// LandingsWait is the look-in for gate j, run in the lead's checkout for ran, which has held the landings
// held (the run's) and others (how many of other baton sessions') for long, at now.
func LandingsWait(j state.Job, ran time.Duration, held []HeldLanding, others int, now time.Time) string {
	var waits []string
	for i, h := range held {
		w := fmt.Sprintf("%s's since %s (%s)", h.Phase, h.Since.Format("15:04"), Span(now.Sub(h.Since)))
		if i == 0 {
			w = fmt.Sprintf("%s's work has waited to land since %s (%s)", h.Phase, h.Since.Format("15:04"), Span(now.Sub(h.Since)))
		}
		waits = append(waits, w)
	}
	if others > 0 {
		waits = append(waits, fmt.Sprintf("%d %s of another baton session's", others, plural(others, "phase", "phases")))
	}
	return fmt.Sprintf("[baton] Your gate in the working tree, job %s (`%s`, %s in), holds landings: %s. They land once it ends. "+
		"If what it checks need not read this working tree, a gate in a lane holds no landing: stop this one (`baton stop %s`) "+
		"and verify in a lane (`baton verify -- %s`, without --here).", j.ID, j.Command, Span(ran), andList(waits), j.ID, j.Command)
}

// LandingsWaitSaid is the line that tells the human about the landings gate j holds.
func LandingsWaitSaid(j state.Job, held []HeldLanding, others int) string {
	var names []string
	for _, h := range held {
		names = append(names, h.Phase)
	}
	if others > 0 {
		names = append(names, fmt.Sprintf("%d %s of another baton session", others, plural(others, "phase", "phases")))
	}
	return fmt.Sprintf("baton: %s %s long to land behind job %s (`%s`), a gate in the checkout", andList(names),
		plural(len(held)+others, "has waited", "have waited"), j.ID, j.Command)
}

// FileConflicts is the look-in that tells the lead that paths each conflicted in a second landing of the
// run; phases are, by path, the phases whose landings conflicted in it, in order, and naming the phases
// not started whose sections name it (conflict.go). replans says baton replans those at the lead's next
// phase boundary (auto_replan); otherwise the plan stays as it is. Either way the lead has nothing to do,
// and nothing to carry to an agent: each agent was told of its own conflict.
func FileConflicts(paths []string, phases, naming map[string][]string, replans bool) string {
	var each []string
	for _, p := range paths {
		each = append(each, fmt.Sprintf("%s (%s; named by %s, not started)", p, strings.Join(phases[p], ", then "), andList(naming[p])))
	}
	next := "auto_replan is off, so baton leaves the plan as it is."
	if replans {
		next = "baton opens a replan of the phases not started at your next phase boundary, to give each of them a file of its own: nothing to do about it before then."
	}
	return fmt.Sprintf("[baton] %s conflicted in a second landing of this run: %s. One file that phases running at once each change serializes them, "+
		"since each landing must merge the last one's changes to it, and conflicts there. %s "+
		"Each landing that conflicted went back to its phase agent with what conflicted and whose changes those were, and the agent merges and resolves it itself: "+
		"nobody needs to carry anything to it.",
		plural(len(paths), "A file", "Files"), andList(each), next)
}

// FileConflictsSaid is the line that tells the human about paths, each in a second landing conflict and
// named by the phases not started naming has for it; replans says baton replans those at the lead's next
// phase boundary.
func FileConflictsSaid(paths []string, naming map[string][]string, replans bool) string {
	var ids []string
	for _, p := range paths {
		for _, id := range naming[p] {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	s := fmt.Sprintf("baton: %s conflicted in a second landing of this run, and %s, not started, %s %s", andList(paths), andList(ids),
		plural(len(ids), "names", "name"), plural(len(paths), "it", "them"))
	if replans {
		s += " → a replan of the phases not started opens at the lead's next phase boundary"
	}
	return s
}

// LookInTyped is the line that wakes an idle lead for look-ins of kinds (state.LookIn's), which its
// prompt's hook gives it.
func LookInTyped(kinds []string) string {
	if anyFailing(kinds) {
		return "[baton] A gate of yours is failing as it runs: baton's look-in, attached, says what it printed and what you can do."
	}
	return "[baton] baton has a look-in for you on a gate or a landing, attached: it says what baton saw and what you can do."
}

// AgentLookIn is the message that wakes an idle phase agent for look-ins of kinds, which come as it ends
// the turn, or with its next tool call.
func AgentLookIn(kinds []string) string {
	const comes = " " + endTurnFor + "it to you then" + orToolCall
	if anyFailing(kinds) {
		return "[baton] A gate of yours is failing as it runs: baton's look-in says what it printed and what you can do." + comes
	}
	return "[baton] baton has a look-in for you on a gate of yours that runs." + comes
}

func anyFailing(kinds []string) bool {
	for _, k := range kinds {
		if k == state.LookFailing {
			return true
		}
	}
	return false
}

// logStill says how long a running gate's log (at log) has not grown: still, negative when not known.
func logStill(still time.Duration, log string) string {
	switch {
	case still < 0:
		return fmt.Sprintf("Its log: %s.", log)
	case still < time.Minute:
		return fmt.Sprintf("Its log (%s) is still growing.", log)
	}
	return fmt.Sprintf("Its log (%s) has not grown for %s.", log, Span(still))
}

// stopsAt says when baton stops a job, at timeout (verify_timeout; none for 0).
func stopsAt(timeout time.Duration) string {
	if timeout <= 0 {
		return ""
	}
	return fmt.Sprintf(" At %s (verify_timeout) baton stops it and counts it as failed.", Span(timeout))
}

// clipLine is a log's line as a look-in quotes it: indented, and at most lookClip bytes, cut between
// characters.
func clipLine(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > lookClip {
		n := lookClip
		for n > 0 && !utf8.RuneStart(line[n]) {
			n--
		}
		line = line[:n] + "…"
	}
	return "    " + line
}
