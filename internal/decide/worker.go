package decide

import (
	"fmt"
	"strings"
	"time"
)

// A worker is a phase agent: an agent of the lead session on one phase of a run, in a git worktree of its
// own, while the lead works on other phases in the human's checkout (docs/research/visible-workers.md).
// baton asks the human its questions, and its work lands on the run's branch under the lead.
// These are the words it reads about all that. None of them names the project's checkout, which is the
// lead's.

// WorkerIntro opens the brief a worker starts from: what a worker is, and where it works (tree, on
// branch, if any).
func WorkerIntro(title, file, phase, tree, branch string) string {
	on := ""
	if branch != "" {
		on = ", on the branch " + branch
	}
	return fmt.Sprintf("You are a worker for the plan %q (%s). baton works on the phases of the plan that can go ahead at the same time, each by an agent of its own: "+
		"you work on %s alone, in a git worktree of your own (%s%s), while the lead session and any other workers work on other phases elsewhere. "+
		"Leave every other phase to them, and work only in this worktree. Commit your work on its branch, and never merge, rebase or push it yourself: "+
		"once you report %s done, baton lands it on the run's branch.", title, file, phase, tree, on, phase)
}

// WorkerTakesUp tells the agent briefed for phase that an agent before it worked on it, and ended before
// it was finished.
func WorkerTakesUp(phase string) string {
	return fmt.Sprintf("An agent before you worked on %s and ended before it was finished: you take the phase up where it stands, in the same worktree.", phase)
}

// WorkerCheckpointed tells the agent briefed for phase that the agent before it checkpointed: it recorded
// where the phase stands and ended, so that the phase goes on with a fresh context.
func WorkerCheckpointed(phase string) string {
	return fmt.Sprintf("The agent before you on %s checkpointed: it recorded where the phase stands and ended, so that you go on with a fresh context, in the same worktree.", phase)
}

// CheckpointNow is the "Now:" line of the brief of an agent that goes on from the checkpoint of the agent
// before it (title: "P3 — Title").
func CheckpointNow(title string) string {
	return fmt.Sprintf("Now: continue %s from where the agent before you left off (see its notes below and the repository's state).", title)
}

// AgentBriefAgain is the message that wakes the phase agent on phase, idle, for the brief baton gives it
// again after its context was compacted: its start hands the brief over.
func AgentBriefAgain(phase string) string {
	return fmt.Sprintf("[baton] Your context was compacted, so baton gives you the brief of %s again, with your next tool call or as you end this turn: "+
		"read it, then go on with the phase from where it stands.", phase)
}

// AgentResumed is the message that starts the phase agent on phase again, with its conversation, once the
// lead's session is back after its claude ended (a crash, or a restart).
func AgentResumed(phase string) string {
	return fmt.Sprintf("[baton] The lead's session is back after its claude ended, and baton starts you again: go on with %s from where it stands. "+
		"A command of yours that was running then was cut off: run it again if you still need it.", phase)
}

// AgentCleared is why a phase agent's phase is taken up by a fresh agent after the human cleared the
// lead's conversation: /clear ends every agent of the session (spike 32).
const AgentCleared = "the lead's conversation was cleared, which ends its agents"

// AgentCheckpointed is why a phase agent's phase is taken up again by a fresh agent, after its
// checkpoint: for the human, and the roster.
const AgentCheckpointed = "its agent checkpointed, and a fresh one goes on from its notes"

// AgentIsBatons is why a model may not message or stop the phase agent on phase (tool, the call it made):
// baton starts, messages and ends its phase agents itself, and nothing a model does keeps one working.
func AgentIsBatons(tool, phase string) string {
	return fmt.Sprintf("[baton] Not run: %s is for baton alone with the agent on %s, one of baton's phase agents. baton starts it, gives it what it needs and ends it itself, "+
		"and lands its work once %s is done. Leave the agent and %s to baton, and go on with your own work.", tool, phase, phase, phase)
}

// PhaseTypeIsBatons is why a model may not start an agent of baton's type (baton:phase): Claude Code
// offers the type, so that it resumes baton's agents by a message, but only baton starts one, for a phase
// it hands out.
const PhaseTypeIsBatons = "[baton] Not run: baton:phase agents are baton's alone. baton starts one for each phase it hands out, in a worktree of its own, " +
	"and lands its work once the phase is done. For help of your own, use another agent type, and go on with your own work."

// AgentGoOn is the one message a phase agent on phase gets after its turn was interrupted (Esc in its
// transcript view, in the lead's terminal): an interrupted agent takes the interruption for a rejection of
// its task (spike 32), and its phase is not over.
func AgentGoOn(phase string) string {
	return fmt.Sprintf("[baton] Your last turn was interrupted, but nothing was rejected: %s is still yours, and goes on. "+
		"Carry on from where you were, and report as usual when it is done.", phase)
}

// AgentHeld tells the human that they stopped the phase agent on phase (x on its row in the agent panel):
// the phase is theirs now, and waits for them, its work kept in its worktree.
func AgentHeld(phase string) string {
	return fmt.Sprintf("baton: the phase agent on %s was stopped from the agent panel, so %s waits for you, its work kept in its worktree; "+
		"/baton run starts a fresh agent on it", phase, phase)
}

// AgentAfresh tells the human that a fresh phase agent takes phase up, as they asked (why: what became of
// the one before it).
func AgentAfresh(phase, why string) string {
	return fmt.Sprintf("baton: a fresh phase agent takes %s up, from its notes and in its worktree (%s); nothing of it is yours to do", phase, why)
}

// AgentWaits is the notice that the phase agent on phase waits for the human to answer a dialog of tool's
// (a permission prompt, mostly), in the lead's terminal.
func AgentWaits(phase, tool string) string {
	return fmt.Sprintf("the phase agent on %s waits for your answer (%s), in the lead's terminal", phase, tool)
}

// WorkerRole reminds a worker, after a compaction, which phase it works on and where.
func WorkerRole(title, file, phase, tree string) string {
	return fmt.Sprintf("You are a worker on %s of the plan %q (%s), in a git worktree of your own (%s): the lead session and any other workers work on the other phases elsewhere.",
		phase, title, file, tree)
}

// WorkingPhases tells the lead's model about the phases baton's workers have (names, as "P3 — Title"):
// baton lands their work by itself, and the phases that need them wait until it has, so it leaves them
// alone.
func WorkingPhases(names []string) string {
	return "Worked on by baton's workers meanwhile, each in a worktree and on a branch of its own: " + strings.Join(names, "; ") +
		". baton lands each one's work on this branch by itself, and tells you when it has; the phases that need them wait until then. Leave these phases alone."
}

// WorkersAttached says, as a plan is attached, how many workers will work beside the lead session: up to
// n, the most that may run at once (workers' number, or with auto what this machine allows), and no more
// than the phases that can be worked on at once allow (width), less the lead's. mode is the permission
// mode they start in, the lead session's ("" for Claude Code's default): one that asks before tools makes
// them stop at prompts, which only the human can answer, in the lead's terminal. It is the human's to
// read: the caller says so, and who it is from. "" with workers off.
func WorkersAttached(n int, auto bool, width int, mode string) string {
	switch {
	case n == 0 && !auto:
		return ""
	case n == 0:
		return "no worker runs beside this session: with workers: auto, a machine needs 4 CPUs and 8 GiB of memory for one, and this one has less. " +
			"To run them anyway, set workers to a number in baton's config.json."
	}
	k := min(n, width-1)
	if k <= 0 {
		return "no worker runs beside this session: each phase of this plan needs the one before it."
	}
	why := fmt.Sprintf("workers: %d", n)
	if auto {
		why = fmt.Sprintf("the plan's widest step is %d phases; this machine allows %d", width, n)
	}
	s := fmt.Sprintf("up to %d %s %s beside this session on the phases that can go ahead, each in a worktree of its own, and baton lands their work on this branch (%s).",
		k, plural(k, "worker", "workers"), plural(k, "works", "work"), why)
	switch mode {
	case "", "default", "acceptEdits", "plan":
		if mode == "" {
			mode = "default"
		}
		s += fmt.Sprintf(" Workers work in this session's permission mode (%s as it started), so a permission prompt stops a worker until you answer it, here in this terminal; "+
			"only its phase waits. To keep them going, start baton with --permission-mode auto, or allow what the plan needs in .claude/settings.json.", mode)
	}
	return s
}

// WorkerHeld tells the human that phase, which could go ahead, gets no worker for now, and why: no room
// for another session on the machine, or in the account's usage.
func WorkerHeld(phase, why string) string {
	return fmt.Sprintf("baton: no worker on %s for now: %s. One starts once there is room; the workers at work carry on", phase, why)
}

// WorkerRefused is why command, which belongs to the lead session, is refused in a worker on phase.
func WorkerRefused(command, phase string) string {
	return fmt.Sprintf("not run — you are a worker: you work on %s alone, in a worktree of your own, while the lead session runs the plan in the human's terminal, "+
		"and baton %s belongs to the lead session. Carry on with %s, and when it is complete and committed, run baton done %s: baton lands your work on the run's branch.",
		phase, command, phase, phase)
}

// WorkerEditRefused is why a worker on phase may not edit a file in the project's checkout, which is the
// lead's; mine is the same file in the worker's own worktree.
func WorkerEditRefused(phase, mine string) string {
	return fmt.Sprintf("[baton] Not here: that file is in the lead session's checkout, which a worker never changes. "+
		"You are a worker on %s, and work only in your own worktree: edit %s instead.", phase, mine)
}

// WorkerPlanRefused is why a worker on phase does not plan: its phase is planned already, and only the
// lead session attaches plans.
func WorkerPlanRefused(phase string) string {
	return fmt.Sprintf("[baton] You are a worker on %s: your phase is planned already, in the run's plan, and only the lead session attaches plans. "+
		"Carry on with %s as the plan describes it.", phase, phase)
}

// WorkerProposed acknowledges a worker's proposal id, which baton puts to the human. untimed
// says why it has no deadline ("" for a timed one); a timed one goes ahead timeout after the human is
// asked.
func WorkerProposed(id, untimed string, timeout time.Duration) string {
	how := fmt.Sprintf("If nobody answers within %s of being asked, baton picks Go ahead for them, and you do it.", Spell(timeout))
	if untimed != "" {
		how = "It has no deadline: " + untimed + ", so only the human can let it go ahead."
	}
	return fmt.Sprintf("proposal %s recorded. baton puts it to the human, and gives you their answer here. %s "+
		"Until then, do not do what you proposed: keep working on what does not depend on it, or, with nothing else to do, commit your work and end your turn.", id, how)
}

// WorkerAwaits is why a worker may not move on (done, blocked, another proposal) while its proposal id,
// to do what, waits for the human's answer.
func WorkerAwaits(id, what string) string {
	return fmt.Sprintf("your proposal %s (%s) waits for the human's answer, which baton gives you here. "+
		"Until then, keep working on what does not depend on it, or end your turn.", id, what)
}

// WorkerBlocked acknowledges a worker's block on phase, which baton puts to the human.
func WorkerBlocked(phase string) string {
	return fmt.Sprintf("recorded. baton puts it to the human; it stops only %s, while the lead session and any other workers carry on. "+
		"Their answer reaches you here. End your turn now.", phase)
}

// WorkerDone is what a worker hears once phase is finished: baton lands its work. job is the verification
// still running ("" for none), which has run for ran.
func WorkerDone(phase, job, ran string) string {
	if job != "" {
		return fmt.Sprintf("%s is finished, and verifying: job %s (%s) is still running. baton lands your work on the run's branch once it passes; "+
			"if it fails, %s reopens here, and you take it up again from your notes on that job. End your turn now.", phase, job, ran, phase)
	}
	return fmt.Sprintf("%s done. baton lands your work on the run's branch: it rebases your branch onto it and merges it under the lead session. "+
		"If anything needs you first (a rebase conflict, a red verification), baton tells you here. End your turn now.", phase)
}

// DropWorkers is what `baton drop` says about the workers it ends: phases names them.
func DropWorkers(phases string) string {
	return fmt.Sprintf("baton also ends the workers on %s: it saves their uncommitted work and removes their worktrees, and keeps the branch of any that holds commits that never landed.", phases)
}

// Left is a worker an earlier run left behind, which baton ended as a new run started: its phase, the
// branch it kept for the Unlanded commits on it that never landed ("" for none), and its worktree, which
// stayed in place for the reason Kept gives ("" when it was removed).
type Left struct {
	Phase, Branch  string
	Unlanded       int
	Worktree, Kept string
}

// WorkersRetired tells the human, in the session of a run that has just started, about the workers an
// earlier run of the project left behind, which baton ended (left, in the order it ended them): what of
// theirs it kept, and where.
func WorkersRetired(left []Left) string {
	phases := make([]string, len(left))
	for i, l := range left {
		phases[i] = l.Phase
	}
	s := fmt.Sprintf("baton: ended %d %s left by an earlier run (%s)", len(left), plural(len(left), "worker", "workers"), strings.Join(phases, ", "))
	for _, l := range left {
		if l.Branch != "" && l.Unlanded > 0 {
			s += fmt.Sprintf("; its branch %s keeps %d %s that never landed", l.Branch, l.Unlanded, plural(l.Unlanded, "commit", "commits"))
		}
	}
	for _, l := range left {
		if l.Kept != "" {
			s += fmt.Sprintf("; the worktree of its worker on %s stays at %s, as %s", l.Phase, l.Worktree, l.Kept)
		}
	}
	return s
}
