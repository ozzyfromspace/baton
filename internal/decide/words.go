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
	// Worker: the session is a worker's, which works on one phase of the run in a worktree of its own;
	// its work lands on the run's branch, and its questions reach the human through the lead session.
	Worker bool
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

// putToHuman is what becomes of a proposal: who puts it to the human, and how the model hears back.
func (w Words) putToHuman() string {
	if w.Worker {
		return "baton puts it to them through the lead session, with " + w.deadline() + " from when they are asked (their answer reaches you here)"
	}
	return "baton puts it to them with " + w.deadline()
}

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
	fmt.Fprintf(&b, "        a human might choose differently: %s, and no answer means you do it.\n", w.putToHuman())
	b.WriteString("        A proposal is an action that makes progress. Waiting, doing nothing or stopping is not a proposal.\n" +
		"  · baton blocked \"<why>\" --tried \"<what you tried>\"\n" +
		"        only if every way forward is irreversible, or needs a secret only the human holds. ")
	if w.Worker {
		b.WriteString("Your phase stops until they answer, through the lead session.")
	} else {
		b.WriteString("The run stops until they answer.")
	}
	return b.String()
}

// Reporting is how the model reports progress and reaches the human, for the primer at the start of a
// hosted session.
func (w Words) Reporting() string {
	safePoint := "a step finished"
	if w.Git {
		safePoint = "work committed"
	}
	done := "when the current phase is " + w.complete() + ", run `baton done <phase> --notes \"<what later phases need to know>\"` and end your turn — baton then compacts the context and starts the next phase with a fresh brief. "
	blocked := ", and end your turn. "
	unattended := "The plan runs unattended, so do not ask the human questions (AskUserQuestion) of your own: decide and carry on, or use `baton propose`, which gives them a deadline to veto your choice. " +
		"A question baton gives you to put to the human is the exception: ask it as given. "
	pause := "(If the human wants to talk instead of running the plan, they can run /baton pause.)"
	if w.Worker {
		done = "when your phase is " + w.complete() + ", run `baton done <phase> --notes \"<what later phases need to know>\"` and end your turn — " + landing + " "
		blocked = ", and end your turn: it stops only your phase, and baton asks the human through the lead session. "
		unattended = "Nobody watches this session, so do not ask the human questions (AskUserQuestion) of your own: decide and carry on, or use `baton propose`, which gives them a deadline to veto your choice. "
		pause = ""
	}
	return strings.TrimSpace("Report progress only with these commands (Bash tool): " + done +
		"A run must not stop for something it can work around. When something goes wrong, keep the plan moving on a reversible path, and tell the human in the lightest way that fits: " +
		"`baton note \"<what you did>\" --undo \"<how to reverse it>\"` when you already took one and the human should know (the run continues); " +
		"`baton propose \"<what you will do>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"` when a rule or convention of the plan can't be followed right now, or a human might choose differently: " +
		w.putToHuman() + ", and no answer means you do it. " +
		"A proposal is an action that makes progress; waiting, doing nothing or stopping is not a proposal. " +
		"One proposal covers the case in front of you: when the same thing happens again, `baton note` it each time. " +
		"Only if every way forward is irreversible or needs a secret only the human holds: `baton blocked \"<why>\" --tried \"<what you tried>\"`" + blocked +
		"Hand a gate that takes more than about 2 minutes, or needs state of its own (a database, a server), to baton: " + w.verifyHow() + " " +
		"A command that runs long moves to the background by itself and wakes you when it ends: keep working on what does not depend on it, and with nothing left to do, end your turn. " +
		"Keep backticks and $ out of double-quoted notes (the shell would run them), or use single quotes. " +
		unattended +
		"In a long phase, at a safe point (" + safePoint + "), `baton checkpoint --notes \"<where you are>\"` compacts mid-phase. " +
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why. " +
		pause)
}

// proposeThen is what follows a proposal, for a stop that reported nothing.
func (w Words) proposeThen() string {
	if w.Worker {
		return "baton puts it to the human through the lead session and gives you their answer here; no answer by its deadline means you do it"
	}
	return "baton puts it to the human with " + w.deadline() + ", then you do it"
}

// landing is what becomes of a worker's phase once it is done.
const landing = "baton then lands your work on the run's branch, and tells you here if anything needs you first (a rebase conflict, a red verification)."

// verifyHow is how a long gate goes to baton, and what becomes of the phase meanwhile.
func (w Words) verifyHow() string {
	if !w.Git {
		return "`baton await \"<command>\" --notes \"<where the phase stands>\"` runs it from the working tree while you keep working on what does not depend on it, " +
			"and baton gives you the result when it ends. Quick checks run in place."
	}
	return "`baton verify \"<command>\" --notes \"<where the phase stands>\"` runs it on HEAD (commit first) in a lane of its own while you keep working on what does not depend on it, " +
		"and baton gives you the result when it ends; `baton await` does the same for something outside the project, such as a CI run. Quick checks run in place. " +
		"Once the phase is complete and committed, run `baton done` even while its gate still runs: the phase is done when the gate passes, and reopens if it fails."
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
		"`baton done %s --notes \"<what later phases need to know>\"` (the phase is %s, even while a job you handed to baton still runs), "+
		"`baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"` "+
		"(something is in the way and you have a reversible way around it: %s), "+
		"`baton blocked \"<why>\" --tried \"<what you tried>\"` (only if every way forward is irreversible or needs a secret only the human holds), or "+
		"`baton await \"<a command that ends when it is ready>\" --notes \"<where the phase stands>\"` (you are waiting on something outside you, such as a CI run or a deploy: baton runs it and gives you the result).", title, id, finished, w.proposeThen())
}

// BackgroundStop is why baton refuses the first stop while background work of the model's own runs:
// what names the work. Nothing is wrong with the stop itself, so the next one on the same work is allowed.
func (w Words) BackgroundStop(what string) string {
	s := "[baton] You stopped while your background work runs (" + what + "). Keep working on what does not depend on it: Claude Code wakes you when it ends. "
	if w.Git {
		s += "A gate running in the checkout is invalidated by your own edits, so a long or stateful gate goes to baton instead: commit, then `baton verify \"<command>\" --notes \"<where the phase stands>\"` runs it on that commit, and baton gives you the result. "
	}
	return s + "If the phase is " + w.complete() + ", report it with `baton done`; if nothing is left to do until the work ends, end your turn again."
}

// QuestionRefused is why a question of the model's own is refused while a plan runs, and what to do
// instead. call is the exact call for a question baton issued and is waiting on ("" if none).
func (w Words) QuestionRefused(call string) string {
	if call != "" {
		return "[baton] While a plan runs, the only question that may be put to the human is the one baton issued, exactly as issued. " +
			"To ask it, " + call + ". For anything else, decide yourself and carry on, and if the human should know what you decided, `baton note` it."
	}
	head := "[baton] baton is running this plan unattended, so a question would stop the run until someone answers it. "
	if w.Worker {
		head = "[baton] Nobody watches this worker session, so a question would stop your phase until someone opens it. "
	}
	return head +
		"Decide yourself and carry on; if the human should know what you decided, run `baton note \"<what you did>\" --undo \"<how to reverse it>\"`. " +
		"If a human might choose differently, run `baton propose \"<what you will do>\" --because \"<why>\" --undo \"<how to reverse it>\"`: " +
		w.putToHuman() + ", and no answer means you do it. " +
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
	if w.Worker {
		return fmt.Sprintf("When %s is %s, run `baton done %s --notes \"<what later phases need to know>\"` and end your turn: %s "+
			"Gates that take more than about 2 minutes, or need state of their own, go to baton: %s "+
			"If something goes wrong, keep moving on a reversible path and tell the human: `baton note` (you took it), "+
			"`baton propose` (a reversible way around a problem; baton puts it to the human through the lead session, who may veto it by a deadline, and gives you the answer here), "+
			"`baton blocked --tried` (only if every way forward is irreversible; it stops only %s). Never type /compact yourself.", id, w.complete(), id, landing, w.verifyHow(), id)
	}
	return fmt.Sprintf("When %s is %s, run `baton done %s --notes \"<what later phases need to know>\"` and end your turn. "+
		"Gates that take more than about 2 minutes, or need state of their own, go to baton: %s "+
		"If something goes wrong, keep moving on a reversible path and tell the human: `baton note` (you took it), "+
		"`baton propose` (a reversible way around a problem; the human may veto it by a deadline), "+
		"`baton blocked --tried` (only if every way forward is irreversible). Never type /compact yourself.", id, w.complete(), id, w.verifyHow())
}
