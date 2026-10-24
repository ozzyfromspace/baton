package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The human wraps a long run up (v0.8.17, /baton wrap, state/wrap.go): every phase still runs and lands, but
// no gate runs whole a second time. A real run's final QA phase ran its 40–49 minute set whole four times, and
// its human, who wanted the run over, had nothing lighter to say so with than dropping it. Only the human
// starts it (in a turn of theirs in the lead's session), and only they lift it (--off), so a whole repeat it
// refuses has no --why: the human chose it, as they choose a pause, and it waits on nothing.

// WrapUp is what each agent at work, and the lead, is told as the human wraps the run up (state.MessageWrap).
const WrapUp = "[baton] The human is wrapping the run up: finish on the evidence you have. No gate runs whole again; " +
	"rerun only what failed (`baton verify --failed`), then report done."

// WrapReason is why wrapping up stopped a job (Job.Reason): a confirmation run, or a whole repeat.
const WrapReason = "stopped as the human wraps the run up: no gate runs whole a second time"

// WrapBrief is the paragraph a brief of phase carries while the run wraps up: a fresh agent hears of it there,
// since the message went to the agent it replaced, or to none.
func WrapBrief(phase string) string {
	return fmt.Sprintf("The human is wrapping the run up: finish %s on the evidence you have. No gate runs whole a second time: "+
		"a gate whose command already ran whole in %s is refused, and a QA phase's reruns are not confirmed by a full run. "+
		"Rerun only what failed (`baton verify --failed`), then report done. A first run of a gate still runs: it is the phase's evidence.",
		Named(phase), Named(phase))
}

// WrapRefused turns down a whole repeat of r.Prev, the gate's last whole run in phase, while the run wraps up:
// what ran whole and when, what to run instead, and that only the human lifts it.
func WrapRefused(phase string, r Repeat) string {
	p := r.Prev
	how := verdict(p)
	if p.Status == state.JobInterrupted {
		how = "was stopped after it showed a failure"
	}
	var when []string
	if r.Took > 0 {
		when = append(when, Span(r.Took))
	}
	if !p.Ended.IsZero() {
		when = append(when, "ending at "+p.Ended.Local().Format("15:04"))
	}
	s := fmt.Sprintf("not queued — the human is wrapping the run up, so no gate runs whole a second time: job %s ran `%s` whole for %s", p.ID, p.Command, phase)
	if len(when) > 0 {
		s += " (" + strings.Join(when, ", ") + ")"
	}
	s += ", and it " + how + "."
	switch o := r.onlyFailed(); {
	case p.Status == state.JobPassed:
		s += fmt.Sprintf(" That is %s's evidence: report %s done on it.", phase, phase)
	case r.Failed:
		s += " Rerun only what failed instead: `baton verify --failed --notes \"…\"`, which carries the stages that passed."
	case o != "":
		s += " Rerun " + o + "."
	default:
		s += fmt.Sprintf(" Rerun only the tests that failed in it, by name: a command of their own queued with --replace becomes %s's gate, and the human is told.", phase)
	}
	if p.Status != state.JobPassed {
		s += fmt.Sprintf(" Where what fails is not %s's to fix, `baton done %s --despite-red` reports it done as it stands.", phase, phase)
	}
	return s + " --why does not change this: only the human lifts it, with /baton wrap --off."
}

// WrapWholeRefused turns down `--whole` of phase's red gate g's command in a QA phase while the run wraps up:
// the same command without it reruns what failed.
func WrapWholeRefused(phase string, g state.Job) string {
	return fmt.Sprintf("not queued — the human is wrapping the run up, so no gate runs whole a second time: --whole would run `%s` whole again after job %s. "+
		"Leave out --whole: in a QA phase the same command reruns only what failed in %s. --why does not change this: only the human lifts it, with /baton wrap --off.",
		g.Command, g.ID, g.ID)
}

// ConfirmWrapSkipped tells the model, at a QA phase's done over reruns, that no full run confirms them while the
// run wraps up (of: the full run they go back to).
func ConfirmWrapSkipped(phase, of string) string {
	since := ""
	if of != "" {
		since = " (" + of + ")"
	}
	return fmt.Sprintf("no full run follows %s's reruns: the human is wrapping the run up, so %s stands on its reruns since the full run%s.", phase, phase, since)
}

// WrapSettle is a QA phase's confirmation run that wrapping up stopped (Job) or dropped while it was owed (no
// Job): the phase is done on its rerun (Rerun) instead.
type WrapSettle struct {
	Phase, Job, Rerun, Command string
	// Decision is the decision it was recorded as ("" when none was).
	Decision string
}

// WrapDecision is the decision wrapping up records for c: what it did, and how to undo it.
func WrapDecision(c WrapSettle) (what, undo string) {
	on := "its rerun"
	if c.Rerun != "" {
		on += " " + c.Rerun
	}
	if c.Job != "" {
		what = fmt.Sprintf("stopped %s's confirmation run (job %s, `%s`) as the human wrapped the run up: %s is done on %s, which passed", c.Phase, c.Job, c.Command, c.Phase, on)
	} else {
		what = fmt.Sprintf("dropped the full run of `%s` owed to confirm %s's reruns, as the human wrapped the run up: %s is done once %s passes", c.Command, c.Phase, c.Phase, on)
	}
	return what, fmt.Sprintf("reopen %s and run `%s`", c.Phase, c.Command)
}

// WrapStop is a whole repeat that wrapping up stopped (Job), and the whole run before it (Whole).
type WrapStop struct {
	Job   state.Job
	Whole string
}

// Wrapped is what wrapping the run up did, over the lead's run and its agents' sub-runs.
type Wrapped struct {
	// Again: the run was wrapping up already, since Since, so nobody was told again.
	Again bool
	Since time.Time
	// Settled are the confirmation runs stopped or dropped, Stopped the whole repeats stopped, and Kept the first
	// runs of gates left to run.
	Settled []WrapSettle
	Stopped []WrapStop
	Kept    []state.Job
	// Told are the phases whose agents were told (state.MessageWrap), Lead whether the lead's model was.
	Told []string
	Lead bool
}

// WrapSaid is what `baton wrap` prints of w: for the lead's model, which reports it to the human.
func WrapSaid(w Wrapped) string {
	var b strings.Builder
	if w.Again {
		fmt.Fprintf(&b, "baton: the run is wrapping up already, since %s; baton looked again for runs to stop.\n", w.Since.Local().Format("15:04"))
	} else {
		b.WriteString("baton: wrapping the run up. Every phase still runs and lands, but no gate runs whole a second time: confirmation runs are skipped, " +
			"a gate whose command already ran whole in its phase is refused, and only what failed reruns. A phase's first run of its gate keeps going: it is that phase's evidence.\n")
	}
	for _, c := range w.Settled {
		what, undo := WrapDecision(c)
		fmt.Fprintf(&b, "- %s", what)
		if c.Decision != "" {
			fmt.Fprintf(&b, " (decision %s; undo: %s)", c.Decision, undo)
		}
		b.WriteString("\n")
	}
	for _, s := range w.Stopped {
		fmt.Fprintf(&b, "- stopped job %s, a whole rerun of `%s` for %s (it ran whole as job %s)\n", s.Job.ID, s.Job.Command, s.Job.Phase, s.Whole)
	}
	for _, j := range w.Kept {
		fmt.Fprintf(&b, "- kept job %s, %s's first run of `%s`\n", j.ID, j.Phase, j.Command)
	}
	if len(w.Settled)+len(w.Stopped) == 0 {
		b.WriteString("- no confirmation run or whole rerun was under way, or owed\n")
	}
	switch {
	case len(w.Told) > 0 && w.Lead:
		fmt.Fprintf(&b, "- told you and %s: finish on the evidence you have, and rerun only what failed\n", agentsOn(w.Told))
	case len(w.Told) > 0:
		fmt.Fprintf(&b, "- told %s: finish on the evidence you have, and rerun only what failed\n", agentsOn(w.Told))
	case w.Lead:
		b.WriteString("- told you: finish on the evidence you have, and rerun only what failed\n")
	}
	b.WriteString("Report this to the human. /baton wrap --off lifts it.")
	return b.String()
}

// WrapLine is the line the human reads in the session as the run starts wrapping up, or looks again (w.Again).
func WrapLine(w Wrapped) string {
	head := "baton: wrapping the run up → no gate runs whole a second time"
	if w.Again {
		head = "baton: the run wraps up still → looked again for runs to stop"
	}
	var parts []string
	var stopped, dropped []string
	for _, c := range w.Settled {
		if c.Job != "" {
			stopped = append(stopped, c.Job+" ("+c.Phase+"'s confirmation)")
		} else {
			dropped = append(dropped, c.Phase+"'s confirmation")
		}
	}
	for _, s := range w.Stopped {
		stopped = append(stopped, s.Job.ID)
	}
	if len(stopped) > 0 {
		parts = append(parts, "stopped "+andList(stopped))
	}
	if len(dropped) > 0 {
		parts = append(parts, "dropped "+andList(dropped))
	}
	if len(w.Told) > 0 {
		parts = append(parts, "told "+agentsOn(w.Told))
	}
	if len(parts) == 0 {
		return head + ". /baton wrap --off lifts it"
	}
	return head + "; " + strings.Join(parts, "; ") + ". /baton wrap --off lifts it"
}

// agentsOn names the agents on phases: "the agent on P3", "the agents on P3 and P7".
func agentsOn(phases []string) string {
	return plural(len(phases), "the agent on ", "the agents on ") + andList(phases)
}

// WrapEnded is what the human and the lead's model hear as the run stops wrapping up (--off).
const WrapEnded = "baton: the run is no longer wrapping up → a gate runs whole again when asked, and a QA phase's reruns are confirmed again at its done. " +
	"What wrapping up stopped stays stopped: each of its decisions says how to undo it"

// WrapNotOn is what --off says when the run is not wrapping up.
const WrapNotOn = "baton: the run is not wrapping up, so there is nothing to lift."

// WrapTurnRefused turns down `baton wrap` in a turn the human did not start: by is who started the lead's turn
// ("" when none is open).
func WrapTurnRefused(by string) string {
	who := "no turn of theirs is open"
	switch by {
	case "":
	case "baton":
		who = "baton started this turn, not the human"
	case "system":
		who = "a notification started this turn, not the human"
	default:
		who = by + " started this turn, not the human"
	}
	return "not wrapped — only the human wraps the run up, by typing /baton wrap in the lead's session, and " + who + ". Carry on with the plan as it is."
}

// WrapNotHosted turns down `baton wrap` in a session no baton host is above.
const WrapNotHosted = "not wrapped — only the human wraps the run up, by typing /baton wrap in the lead's session, which baton hosts; this session is not hosted by baton."

// WrapNoRun turns down `baton wrap` with no plan running (mode).
func WrapNoRun(mode string) string {
	return fmt.Sprintf("not wrapped — no plan is running here (mode: %s), so there is nothing to wrap up.", mode)
}
