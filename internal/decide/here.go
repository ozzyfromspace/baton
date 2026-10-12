package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A gate that needs the checkout itself (its running services, files that are not in git) says so:
// `baton verify --here` runs it on HEAD in the session's own working tree, with no lane. It reads that tree
// as it runs, so until it ends baton holds what would change it there: edits, another phase, and, in the
// lead's checkout, a worker's work landing (state.HereGate).

// HereQueued is what the model does while its gate runs in its working tree (--here), which baton holds
// until the gate ends; worker: the session is a worker's, in a worktree no landing touches. timeout is
// how long a job may run.
func HereQueued(worker bool, timeout string) string {
	held := "takes up no other phase here, and lands no worker's work in this checkout"
	if worker {
		held = "and takes up nothing else here"
	}
	return fmt.Sprintf("The gate reads this working tree as it runs, so until it ends baton refuses edits to it, %s: "+
		"they would change what it checks. Meanwhile, do only what changes no file here, or end your turn: baton gives you its result when it ends, "+
		"and baton status shows it meanwhile. If it runs past %s, baton stops it and counts it as failed.", held, timeout)
}

// HereKept notes the uncommitted files already in the working tree when phase started, which a gate run
// there (--here) runs with.
func HereKept(n int, phase string) string {
	return fmt.Sprintf("note — %d uncommitted %s already there when %s started; the gate runs with %s, as %s in the working tree.",
		n, plural(n, "file was", "files were"), phase, plural(n, "it", "them"), plural(n, "it is", "they are"))
}

// HereEdit is why an edit is refused while job j, a gate, runs in the working tree it would change.
func HereEdit(j state.Job) string {
	return fmt.Sprintf("[baton] Not now: job %s's gate (`%s`) runs in this working tree and reads its files as it runs, so baton refuses edits here until it ends. "+
		"Meanwhile, do only what changes no file here, or end your turn: baton gives you the result when the gate ends.", j.ID, j.Command)
}

// HereHeld is why no other phase starts after `baton done` while the phase's gate, job j, runs in the
// working tree.
func HereHeld(j state.Job) string {
	return fmt.Sprintf("Job %s runs in this working tree, so no other phase starts here until it ends. End your turn now; baton wakes you when there is something to do.", j.ID)
}

// CheckoutNamed turns down a gate whose command names the checkout (checkout), to run in a lane, where it
// would check the checkout's files as they are, while other work changes them, rather than the commit.
// lanes: the project has a .baton-lane script. For a worker (worktree: its own), the checkout is the
// lead's, which it never touches.
func CheckoutNamed(command, checkout, worktree string, lanes bool) string {
	if worktree != "" {
		return fmt.Sprintf("not queued — `%s` names the lead session's checkout (%s), which a worker never touches: your gates check your own work. "+
			"Leave the checkout's path out: verify runs the command on your commit in a lane of its own, or, with --here, in your worktree (%s).", command, checkout, worktree)
	}
	text := fmt.Sprintf("not queued — `%s` names the checkout (%s), but verify runs a gate in a lane: a checkout of the commit of its own, apart from this one. "+
		"Run from there, the gate would check the files in this checkout as they are, not the commit, while landings and edits change them under it. "+
		"If the gate needs this checkout (its running services, files that are not in git), run it here: "+
		"`baton verify --here \"<command>\" --notes \"…\"` runs it on HEAD from the checkout's root, with no lane, and baton holds edits, other phases and landings here until it ends.",
		command, checkout)
	if !lanes {
		text += " Or, for gates that run in lanes with services of their own, commit an executable .baton-lane script (baton status says how)."
	}
	return text
}
