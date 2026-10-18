package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Words builds the text the model reads about reporting progress and reaching the human. It depends on
// the project: whether git is usable there (gitx.Usable), and how long a proposal waits for the human.
// Without git nothing it says asks for a commit, or for a repository.
type Words struct {
	Git     bool
	Timeout time.Duration
	// Worker: the session is a worker's, which works on one phase of the run in a worktree of its own;
	// its work lands on the run's branch, and baton asks the human its questions.
	Worker bool
	// AggregateQA: the plan's heavy checks wait for its QA phases (aggregate_qa, on by default).
	AggregateQA bool
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
		return "baton puts it to them with " + w.deadline() + " from when they are asked (their answer reaches you here)"
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
	fmt.Fprintf(&b, "        a human might choose differently: %s, and no answer means you do it (%s).\n", w.putToHuman(), outwardNoAnswer)
	b.WriteString("        A proposal is an action that makes progress. Waiting, doing nothing or stopping is not a proposal.\n" +
		"  · baton blocked \"<why>\" --tried \"<what you tried>\"\n" +
		"        only if every way forward is irreversible, or needs a secret only the human holds. ")
	if w.Worker {
		b.WriteString("Your phase stops until they answer, ")
	} else {
		b.WriteString("The run stops until they answer, ")
	}
	b.WriteString(lookAgainAfter + ".")
	return b.String()
}

// Reporting is how the model reports progress and reaches the human, for the primer at the start of a
// hosted session.
func (w Words) Reporting() string {
	safePoint := "a step finished"
	if w.Git {
		safePoint = "work committed"
	}
	done := "when the current phase is " + w.complete() + ", run `" + DoneCall + "` and end your turn — baton then compacts the context and starts the next phase with a fresh brief. "
	blocked := ", and end your turn: " + lookAgainAfter + ". "
	unattended := "The plan runs unattended, so do not ask the human questions (AskUserQuestion) of your own: decide and carry on, or use `baton propose`, which gives them a deadline to veto your choice. " +
		"A question baton gives you to put to the human is the exception: ask it as given. "
	pause := "(If the human wants to talk instead of running the plan, they can run /baton pause.)"
	if w.Worker {
		done = "when your phase is " + w.complete() + ", run `" + DoneCall + "` and end your turn — " + landing + " " + LandCheckHow + " "
		blocked = ", and end your turn: it stops only your phase, and baton asks the human; " + lookAgainAfter + ". "
		unattended = "Do not ask the human questions (AskUserQuestion) of your own: decide and carry on, or use `baton propose`, which baton puts to them with a deadline to veto your choice. "
		pause = ""
	}
	return strings.TrimSpace("Report progress only with these commands (Bash tool): " + done +
		"A run must not stop for something it can work around. When something goes wrong, keep the plan moving on a reversible path, and tell the human in the lightest way that fits: " +
		"`baton note \"<what you did>\" --undo \"<how to reverse it>\"` when you already took one and the human should know (the run continues); " +
		"`baton propose \"<what you will do>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"` when a rule or convention of the plan can't be followed right now, or a human might choose differently: " +
		w.putToHuman() + ", and no answer means you do it (" + outwardNoAnswer + "). " +
		"A proposal is an action that makes progress; waiting, doing nothing or stopping is not a proposal. " +
		"One proposal covers the case in front of you: when the same thing happens again, `baton note` it each time. " +
		"Only if every way forward is irreversible or needs a secret only the human holds: `baton blocked \"<why>\" --tried \"<what you tried>\"`" + blocked +
		"Hand a gate that takes more than about 2 minutes, or needs state of its own (a database, a server), to baton: " + w.verifyHow() + " " +
		"A command that runs long moves to the background by itself and wakes you when it ends: keep working on what does not depend on it, and with nothing left to do, end your turn. " +
		"Keep backticks and $ out of double-quoted notes (the shell would run them), or use single quotes. " +
		unattended +
		"In a long phase, at a safe point (" + safePoint + "), `baton checkpoint --notes \"<where you are>\"` " + w.checkpoints() + ". " +
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why. " +
		pause)
}

// checkpoints is what a checkpoint does: the lead's compacts its context mid-phase, and a phase agent's
// ends it, for a fresh agent that goes on from its notes.
func (w Words) checkpoints() string {
	if w.Worker {
		return "ends you mid-phase, and a fresh agent continues the phase from your notes"
	}
	return "compacts mid-phase"
}

// CheckpointThen is what follows a checkpoint, for the model asked to make one.
func (w Words) CheckpointThen() string {
	if w.Worker {
		return "baton starts a fresh agent on this phase, in this worktree, and it continues the phase from your notes"
	}
	return "baton compacts the context and you continue this phase from your notes"
}

// CheckpointHold is why a call is held once the model has checkpointed (it ends its turn, endTurn).
func (w Words) CheckpointHold() string {
	if w.Worker {
		return "you checkpointed, and a fresh agent continues this phase from your notes"
	}
	return "you asked for a checkpoint and baton must compact the context first"
}

// proposeThen is what follows a proposal, for a stop that reported nothing.
func (w Words) proposeThen() string {
	if w.Worker {
		return "baton puts it to the human and gives you their answer here; no answer by its deadline means you do it"
	}
	return "baton puts it to the human with " + w.deadline() + ", then you do it"
}

// outwardNoAnswer is what no answer means for a proposal that reaches outside the project or can't be
// undone (Outward): baton answers it Not now (NotNowRule).
const outwardNoAnswer = "for an outward or irreversible act, that you do it only if it is safely reversible and stays in the project"

// lookAgainAfter is what becomes of a block nobody answers (BlockLook).
var lookAgainAfter = "if nobody answers in " + Spell(BlockLook) + ", baton lifts the block, and you look again for a safe way on and propose it"

// landing is what becomes of a worker's phase once it is done.
const landing = "baton then lands your work on the run's branch, and tells you here if anything needs you first (a conflict, a red verification)."

// verifyHow is how a long gate goes to baton, and what becomes of the phase meanwhile.
func (w Words) verifyHow() string {
	if !w.Git {
		return "`baton await \"<command>\" --notes \"<where the phase stands>\"` runs it from the working tree while you keep working on what does not depend on it, " +
			"and baton gives you the result when it ends. Quick checks run in place."
	}
	s := w.jobCall("verify", "<command>") + " runs it on HEAD (commit first) in a lane of its own while you keep working on what does not depend on it, " +
		"and baton gives you the result when it ends; `baton await` does the same for something outside the project, such as a CI run. Quick checks run in place. " +
		"Once the phase is complete and committed, run `baton done` even while its gate still runs: the phase is done when the gate passes, and reopens if it fails."
	if w.Worker {
		s += " Give a job's command as its words after `--`, as you would type it: Claude Code refuses a phase agent's command that hands a program text starting with a shell or git, such as `baton verify \"sh check.sh\"`."
	}
	return s
}

// jobCall is how the model hands baton the command what to run as a job of kind (verify, await). A phase
// agent gives it as words after `--` (lane.Join quotes them again), since Claude Code refuses a command
// of an agent isolated in a worktree that hands a program text starting with a shell or git.
func (w Words) jobCall(kind, what string) string {
	if w.Worker {
		return "`baton " + kind + " --notes \"<where the phase stands>\" -- " + what + "`"
	}
	return "`baton " + kind + " \"" + what + "\" --notes \"<where the phase stands>\"`"
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
		"%s (you are waiting on something outside you, such as a CI run or a deploy: baton runs it and gives you the result).", title, id, finished, w.proposeThen(), w.jobCall("await", "<a command that ends when it is ready>"))
}

// BackgroundStop is why baton refuses the first stop while background work of the model's own runs whose
// start baton did not see (BackgroundStarted): what names the work. Nothing is wrong with the stop itself,
// so the next one on the same work is allowed.
func (w Words) BackgroundStop(what string) string {
	return "[baton] You stopped while your background work runs (" + what + "). " + w.meanwhile("Claude Code wakes you when it ends", "the work ends", "end your turn again")
}

// BackgroundStarted is what baton tells the model with the result of a tool call that started background
// work of its own (what names it; monitor: a Monitor, which wakes it with what it reports): what
// BackgroundStop would tell it at its stop, told as the work starts, so that stop is allowed.
func (w Words) BackgroundStarted(what string, monitor bool) string {
	if monitor {
		return "[baton] Your monitor runs (" + what + "). " + w.meanwhile("Claude Code wakes you with what it reports", "it reports", "end your turn")
	}
	return "[baton] Your background work runs (" + what + "). " + w.meanwhile("Claude Code wakes you when it ends", "the work ends", "end your turn")
}

// meanwhile is what the model does while its background work runs: wakes says how the work wakes it, and
// end how its turn ends when nothing is left to do until then. Since the work wakes it, nothing else
// need wait for it: a loop or another background task started only to poll it (a sleep, a Monitor, an
// agent) spends turns and tasks on nothing.
func (w Words) meanwhile(wakes, until, end string) string {
	s := "Keep working on what does not depend on it: " + wakes + ". " +
		"Never start a loop or another background task only to wait for it. "
	if w.Git {
		s += "A gate running in the checkout is invalidated by your own edits, so a long or stateful gate goes to baton instead: commit, then " + w.jobCall("verify", "<command>") + " runs it on that commit, and baton gives you the result. "
	}
	return s + "If the phase is " + w.complete() + ", report it with `baton done`; if nothing is left to do until " + until + ", " + end + "."
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
		head = "[baton] baton runs your phase unattended, so a question would stop it until someone answers it. "
	}
	return head +
		"Decide yourself and carry on; if the human should know what you decided, run `baton note \"<what you did>\" --undo \"<how to reverse it>\"`. " +
		"If a human might choose differently, run `baton propose \"<what you will do>\" --because \"<why>\" --undo \"<how to reverse it>\"`: " +
		w.putToHuman() + ", and no answer means you do it (" + outwardNoAnswer + "). " +
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
			"`baton propose` (a reversible way around a problem; baton puts it to the human, who may veto it by a deadline, and gives you the answer here), "+
			"`baton blocked --tried` (only if every way forward is irreversible; it stops only %s). Never type /compact yourself.", id, w.complete(), id, landing, w.verifyHow(), id)
	}
	return fmt.Sprintf("When %s is %s, run `baton done %s --notes \"<what later phases need to know>\"` and end your turn. "+
		"Gates that take more than about 2 minutes, or need state of their own, go to baton: %s "+
		"If something goes wrong, keep moving on a reversible path and tell the human: `baton note` (you took it), "+
		"`baton propose` (a reversible way around a problem; the human may veto it by a deadline), "+
		"`baton blocked --tried` (only if every way forward is irreversible). Never type /compact yourself.", id, w.complete(), id, w.verifyHow())
}

// QAFocus tells a phase that is not a QA phase how to check its work while the plan aggregates its heavy
// checks: qa is the next QA phase ("P5 (QA after the parser)"). It protects the human's time: dense
// checks at every phase burn hours and hold everything behind them, and the model does not feel them
// (TimeFelt).
func QAFocus(qa string) string {
	return "QA: this plan runs its heavy checks in QA phases, to spare the human's time; the next is " + qa + ". " +
		"In this phase, run checks that finish in seconds or minutes: the tests of what you changed, by name. " +
		"No full suite, end-to-end run or whole-project lint before " + qa + ", even where a convention says to run them before every commit; " +
		"commit work in progress all the same. " +
		"A full run is one tool call to you; the human feels every minute of it, and " + qa + " runs it anyway. " +
		"A full check now only for a particular reason, flagged: `baton verify --qa \"<why now>\" --notes \"…\" -- <command>`."
}

// QAPhase tells a QA phase what it is for: since lists the phases it checks. The heavy checks run once:
// a real run's QA phase spent 8 hours on three full passes of one set (TimeFelt).
func QAPhase(since string) string {
	s := "QA: this is a QA phase. Run the full test suites, lints and the other checks the plan names, and fix what they find"
	if since != "" {
		s += " in the work of " + since + ", which ran scoped checks only: expect things that do not fit together yet"
	}
	return s + ". The heavy checks run here, once: after a red, rerun only what failed. " +
		"A full pass costs the human its whole length, though to you it is one tool call: never run the set again to re-prove what passed. " +
		"Push to CI early, batching fixes per round trip. Finish when they pass."
}

// QAAsk is baton's standard question to a phase that queues a job of kind (state.JobVerify, state.JobAwait)
// before the plan's QA phase qa ("P5 (QA)"): baton cannot tell a focused check from a full one, so the
// model decides, and nothing is refused. A wait on CI is often a full check in disguise.
func QAAsk(kind, phase, qa string) string {
	what := "this a focused check of what " + phase + " changed, or a full suite, lint or end-to-end run"
	if kind == state.JobAwait {
		what = "what this waits on a focused check of what " + phase + " changed, or a full check in disguise (a CI run of every suite, a lint or end-to-end run)"
	}
	return "Is " + what + " that " + qa + " runs anyway? " +
		"If it is the latter and nothing in particular calls for it now, leave such runs to " + qa + ": their time is the human's. " +
		"The job runs either way. A full check you run early on purpose is flagged: baton " + kind + " \"<command>\" --qa \"<why now>\" --notes \"…\"."
}

// QAEarly tells the human that phase runs a full check ahead of the plan's QA phase, as job id, and why.
func QAEarly(phase, id, why string) string {
	return fmt.Sprintf("baton: %s runs a full check ahead of its QA phase (job %s): %s", phase, id, why)
}

// CompetingDirectives is baton's request to the model, once per plan, before the human sees the plan for
// approval: instructions baton does not control can pull against the plan, or against how baton runs it,
// and the run is far cheaper to set right now than hours into it.
func CompetingDirectives() string {
	return "[baton] Before the human approves this plan, check it against the instructions baton does not control: the CLAUDE.md files " +
		"(the project's, its parents', ~/.claude/CLAUDE.md), the memory index MEMORY.md and the memories it names, skills, and settings. " +
		"List every directive that competes with the plan or with how baton runs it: for example, a rule to run the full test suite or lint " +
		"before every commit (against this plan's QA phases, which aggregate those checks), a rule to ask the human before something (against " +
		"a run nobody watches), or a rule about models, tools or git. Address each one in the plan: in its Standing rules, which win for this " +
		"run, or by naming the instruction the human should change. If none competes, say so in one line of the plan's Standing rules. " +
		"Then call ExitPlanMode again; baton asks this once per plan."
}

// ExitPlanAgain sends a model that stopped after baton turned its plan back (competing directives, or a
// plan baton could not attach) back to put its plan to the human: nobody would see it otherwise.
func ExitPlanAgain() string {
	return "[baton] You stopped before putting the plan to the human. Make the changes baton asked for in the plan if you have not, then call ExitPlanMode now, so they can approve it."
}

// maxUnbuilt bounds how many of a plan's problems PlanUnbuilt lists.
const maxUnbuilt = 10

// PlanUnbuilt turns back a plan baton could not attach as written (why, what plan.Build found, one
// problem a line), before the human approves it: the model fixes the plan and puts it again. The same
// text again goes to the human as it is.
func PlanUnbuilt(why []string) string {
	if len(why) > maxUnbuilt {
		why = append(why[:maxUnbuilt:maxUnbuilt], fmt.Sprintf("and %d more", len(why)-maxUnbuilt))
	}
	return "[baton] baton could not attach this plan as written, so the human's approval would not start it:\n\n- " + strings.Join(why, "\n- ") +
		"\n\nFix the plan: each phase a heading of its own (`## P0 — Title`) with an id no other phase has, and each `After:` line naming only " +
		"earlier phases of this plan (or `none`, or `all`). Then call ExitPlanMode again. baton turns a plan back for this once per text: " +
		"the same plan again goes to the human as it is, and its approval says it was not attached."
}

// PlanUnbuiltSaid tells the human why Claude takes the plan back before they see it: baton could not
// attach it as written (first, its first problem).
func PlanUnbuiltSaid(first string) string {
	return "baton: the plan would not attach as written (" + first + ") → Claude fixes it before you see it"
}
