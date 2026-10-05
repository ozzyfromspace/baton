package decide

import (
	"fmt"
	"strings"
	"time"
)

// Words builds the text the model reads about reporting progress and reaching the human. It depends on
// the project: whether git is usable there (gitx.Usable), and how long a proposal waits for the human.
// Without git nothing it says asks for a commit, or for a repository.
type Words struct {
	Git     bool
	Timeout time.Duration
}

// complete is what a finished phase is.
func (w Words) complete() string {
	if w.Git {
		return "complete and committed"
	}
	return "complete"
}

// deadline is how long the human has to veto a proposal.
func (w Words) deadline() string { return "a deadline of " + Spell(w.Timeout) }

// Playbook is what a refusal says instead of only "no": how to keep the run moving, and the lightest
// way to reach the human that fits (the wording held up in the S2 replay, docs/research/escalation.md).
// Without git there is no "uncommitted work?" line.
func (w Words) Playbook() string {
	var b strings.Builder
	b.WriteString("A run must not stop for something it can work around. Before hard-blocking:\n")
	if w.Git {
		b.WriteString("  · uncommitted work?   commit it, degraded if need be (e.g. --no-gpg-sign); never discard, stash, reset or unstage work\n")
	}
	b.WriteString("  · a step failed?      is there a worse-but-working version? do that\n" +
		"  · a rule or convention of the plan can't be followed right now (a tool missing, a service down)?\n" +
		"                        that is what baton propose is for: the human gets a deadline to veto your way around it\n" +
		"Then pick the lightest status that fits:\n" +
		"  · baton note \"<what you did>\" --undo \"<how to reverse it>\"\n" +
		"        you already took a reversible path, and the human should know. The run continues.\n" +
		"  · baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"\n")
	fmt.Fprintf(&b, "        a human might choose differently: baton puts it to them with %s, and no answer means you do it.\n", w.deadline())
	b.WriteString("        A proposal is an action that makes progress. Waiting, doing nothing or stopping is not a proposal.\n" +
		"  · baton blocked \"<why>\" --tried \"<what you tried>\"\n" +
		"        only if every way forward is irreversible, or needs a secret only the human holds. The run stops until they answer.")
	return b.String()
}

// Reporting is how the model reports progress and reaches the human, for the primer at the start of a
// hosted session.
func (w Words) Reporting() string {
	safePoint := "a step finished"
	if w.Git {
		safePoint = "work committed"
	}
	return "Report progress only with these commands (Bash tool): " +
		"when the current phase is " + w.complete() + ", run `baton done <phase> --notes \"<what later phases need to know>\"` and end your turn — baton then compacts the context and starts the next phase with a fresh brief. " +
		"A run must not stop for something it can work around. When something goes wrong, keep the plan moving on a reversible path, and tell the human in the lightest way that fits: " +
		"`baton note \"<what you did>\" --undo \"<how to reverse it>\"` when you already took one and the human should know (the run continues); " +
		"`baton propose \"<what you will do>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"` when a rule or convention of the plan can't be followed right now, or a human might choose differently: " +
		"baton puts it to them with " + w.deadline() + ", and no answer means you do it. " +
		"A proposal is an action that makes progress; waiting, doing nothing or stopping is not a proposal. " +
		"One proposal covers the case in front of you: when the same thing happens again, `baton note` it each time. " +
		"Only if every way forward is irreversible or needs a secret only the human holds: `baton blocked \"<why>\" --tried \"<what you tried>\"`, and end your turn. " +
		"If you must wait for something outside you (a build, a deploy, background work), run `baton waiting \"<what>\" --until <duration>` (at most 2h): background work alone is not a status. " +
		"Keep backticks and $ out of double-quoted notes (the shell would run them), or use single quotes. " +
		"The plan runs unattended, so do not ask the human questions (AskUserQuestion) yourself: decide and carry on, or use `baton propose`, which gives them a deadline to veto your choice. " +
		"In a long phase, at a safe point (" + safePoint + "), `baton checkpoint --notes \"<where you are>\"` compacts mid-phase. " +
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why. " +
		"(If the human wants to talk instead of running the plan, they can run /baton pause.)"
}

// NoStatus is why baton refuses a stop that reported nothing, and the statuses to report with.
func (w Words) NoStatus(id, title string) string {
	finished := "finished"
	if w.Git {
		finished = "finished and committed"
	}
	return fmt.Sprintf("[baton] You stopped, but phase %s is not reported done. If the human asked you something this turn, answer it first. "+
		"If there is more to do on the phase, keep working on it (if you took a reversible step the human should know about, `baton note` it and carry on). "+
		"Otherwise report with exactly one of these, then end your turn: "+
		"`baton done %s --notes \"<what later phases need to know>\"` (the phase is %s), "+
		"`baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"` "+
		"(something is in the way and you have a reversible way around it: baton puts it to the human with %s, then you do it), "+
		"`baton blocked \"<why>\" --tried \"<what you tried>\"` (only if every way forward is irreversible or needs a secret only the human holds), or "+
		"`baton waiting \"<what>\" --until <duration>` (you are waiting on something outside you).", title, id, finished, w.deadline())
}

// QuestionRefused is why a question of the model's own is refused while a plan runs, and what to do
// instead. call is the exact call for a question baton issued and is waiting on ("" if none).
func (w Words) QuestionRefused(call string) string {
	if call != "" {
		return "[baton] While a plan runs, the only question that may be put to the human is the one baton issued, exactly as issued. " +
			"To ask it, " + call + ". For anything else, decide yourself and carry on, and if the human should know what you decided, `baton note` it."
	}
	return "[baton] baton is running this plan unattended, so a question would stop the run until someone answers it. " +
		"Decide yourself and carry on; if the human should know what you decided, run `baton note \"<what you did>\" --undo \"<how to reverse it>\"`. " +
		"If a human might choose differently, run `baton propose \"<what you will do>\" --because \"<why>\" --undo \"<how to reverse it>\"`: " +
		"baton puts it to them with " + w.deadline() + ", and no answer means you do it. " +
		"Only if every way forward is irreversible or needs a secret only the human holds: `baton blocked \"<why>\" --tried \"<what you tried>\"`."
}

// FinishStep is how the model gets to a safe point before a checkpoint.
func (w Words) FinishStep() string {
	if w.Git {
		return "Finish the step you are on and commit it"
	}
	return "Finish the step you are on"
}

// Closing ends the brief after a compaction: how to report phase id.
func (w Words) Closing(id string) string {
	return fmt.Sprintf("When %s is %s, run `baton done %s --notes \"<what later phases need to know>\"` and end your turn. "+
		"If something goes wrong, keep moving on a reversible path and tell the human: `baton note` (you took it), "+
		"`baton propose` (a reversible way around a problem; the human may veto it by a deadline), "+
		"`baton blocked --tried` (only if every way forward is irreversible). "+
		"Waiting on something outside you: `baton waiting \"<what>\" --until <duration>`. Never type /compact yourself.", id, w.complete(), id)
}
