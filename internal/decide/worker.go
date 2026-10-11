package decide

import (
	"fmt"
	"strings"
	"time"
)

// A worker is a session of its own on one phase of a run, in a git worktree of its own, while the lead
// session works on other phases in the human's checkout (docs/research/workers.md). Nobody watches a
// worker: its questions reach the human through the lead, and its work lands on the run's branch under
// the lead. These are the words it reads about all that. None of them names the project's checkout,
// which is the lead's.

// WorkerIntro opens the brief a worker starts from: what a worker is, and where it works (tree, on
// branch, if any).
func WorkerIntro(title, file, phase, tree, branch string) string {
	on := ""
	if branch != "" {
		on = ", on the branch " + branch
	}
	return fmt.Sprintf("You are a worker for the plan %q (%s). baton works on the phases of the plan that can go ahead at the same time, each in a session of its own: "+
		"this session works on %s alone, in a git worktree of its own (%s%s), while the lead session and any other workers work on other phases elsewhere. "+
		"Leave every other phase to them, and work only in this worktree. Commit your work on its branch, and never merge, rebase or push it yourself: "+
		"once you report %s done, baton lands it on the run's branch.", title, file, phase, tree, on, phase)
}

// WorkerHosted opens the primer of a worker's session that resumed: what it is.
func WorkerHosted(title, phase string) string {
	return fmt.Sprintf("This session is hosted by baton, as a worker for the attached plan %q: it works on %s alone, in a git worktree of its own, "+
		"while the lead session and any other workers work on other phases elsewhere. ", title, phase)
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
// n (workers), and no more than the phases that can be worked on at once allow (width), less the lead's.
// mode is the permission mode they start in ("" for Claude Code's default): one that asks before tools
// makes them stop at prompts, which only the human can answer, in the worker's own session. local: the
// project has a .claude/settings.local.json, which a worktree lacks. It is the human's to read: the
// caller says so, and who it is from. "" with no workers.
func WorkersAttached(n, width int, mode string, local bool) string {
	if n == 0 {
		return ""
	}
	k := min(n, width-1)
	if k <= 0 {
		return "no worker runs beside this session: each phase of this plan needs the one before it."
	}
	s := fmt.Sprintf("up to %d %s %s beside this session on the phases that can go ahead, each in a worktree of its own, and baton lands their work on this branch (workers: %d).",
		k, plural(k, "worker", "workers"), plural(k, "works", "work"), n)
	switch mode {
	case "", "default", "acceptEdits", "plan":
		if mode == "" {
			mode = "default"
		}
		s += fmt.Sprintf(" Workers start in the permission mode this session started in (%s), so a permission prompt stops a worker until you answer it in its own session "+
			"(baton view <phase>, in another terminal); only its phase waits. To keep them going, start baton with --permission-mode auto, or allow what the plan needs in .claude/settings.json.", mode)
	}
	if local {
		s += " Workers do not see .claude/settings.local.json: their worktrees hold only what is committed."
	}
	return s
}

// WorkerRefused is why command, which belongs to the lead session, is refused in a worker on phase.
func WorkerRefused(command, phase string) string {
	return fmt.Sprintf("not run — this session is a worker: it works on %s alone, in a worktree of its own, while the lead session runs the plan in the human's terminal, "+
		"and baton %s belongs to the lead session. Carry on with %s, and when it is complete and committed, run baton done %s: baton lands your work on the run's branch.",
		phase, command, phase, phase)
}

// WorkerEditRefused is why a worker on phase may not edit a file in the project's checkout, which is the
// lead's; mine is the same file in the worker's own worktree.
func WorkerEditRefused(phase, mine string) string {
	return fmt.Sprintf("[baton] Not here: that file is in the lead session's checkout, which a worker never changes. "+
		"This session is a worker on %s, and works only in its own worktree: edit %s instead.", phase, mine)
}

// WorkerPlanRefused is why a worker on phase does not plan: its phase is planned already, and only the
// lead session attaches plans.
func WorkerPlanRefused(phase string) string {
	return fmt.Sprintf("[baton] This session is a worker on %s: its phase is planned already, in the run's plan, and only the lead session attaches plans. "+
		"Carry on with %s as the plan describes it.", phase, phase)
}

// WorkerProposed acknowledges a worker's proposal id, which the lead session puts to the human. untimed
// says why it has no deadline ("" for a timed one); a timed one goes ahead timeout after the human is
// asked.
func WorkerProposed(id, untimed string, timeout time.Duration) string {
	how := fmt.Sprintf("If nobody answers within %s of being asked, baton picks Go ahead for them, and you do it.", Spell(timeout))
	if untimed != "" {
		how = "It has no deadline: " + untimed + ", so only the human can let it go ahead."
	}
	return fmt.Sprintf("proposal %s recorded. Nobody watches this session, so baton puts it to the human through the lead session, and gives you their answer here. %s "+
		"Until then, do not do what you proposed: keep working on what does not depend on it, or, with nothing else to do, commit your work and end your turn.", id, how)
}

// WorkerAwaits is why a worker may not move on (done, blocked, another proposal) while its proposal id,
// to do what, waits for the human's answer.
func WorkerAwaits(id, what string) string {
	return fmt.Sprintf("your proposal %s (%s) waits for the human's answer, which baton gets through the lead session and gives you here. "+
		"Until then, keep working on what does not depend on it, or end your turn.", id, what)
}

// WorkerBlocked acknowledges a worker's block on phase, which the lead session puts to the human.
func WorkerBlocked(phase string) string {
	return fmt.Sprintf("recorded. baton puts it to the human through the lead session; it stops only %s, while the lead session and any other workers carry on. "+
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

// WorkerBegin is the prompt a worker's session is started with. Its brief reaches it first, from the
// SessionStart hook.
func WorkerBegin(phase string) string {
	return fmt.Sprintf("[baton] Begin %s — the brief above has your instructions.", phase)
}

// WorkerResume is the prompt a worker's session is resumed with, in its own worktree: why says why its
// session ended.
func WorkerResume(phase, why string) string {
	return fmt.Sprintf("[baton] baton resumed this session: %s. Carry on with %s where you left off: check git status and your notes first.", why, phase)
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
