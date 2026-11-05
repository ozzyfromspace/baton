package decide

import "time"

// BackgroundCheck is why baton refuses, once, a check (lane.IsCheck) the model starts as a background shell
// task of its own while its plan runs, command naming it: baton sees nothing of such a task, so nothing
// records it, it holds no lane, and the model learns of a failure only when it ends. A long check belongs in
// baton verify, where it is recorded, holds a lane, and its failures reach the model as they happen; a quick
// one runs in the foreground. The same call again goes through, so this is information, never a wall.
func (w Words) BackgroundCheck(command string) string {
	how := "commit, then " + w.jobCall("verify", "<command>") + " runs it on that commit in a lane of its own, records it, and tells you of its failures as they happen, while you keep working on what does not depend on it."
	if !w.Git {
		how = w.jobCall("await", "<command>") + " runs it from the working tree, records it, and gives you the result when it ends, while you keep working on what does not depend on it."
	}
	return "[baton] Not run yet: this starts a check (" + command + ") as a background task of your own, which baton cannot see: nothing records it, it holds no lane, and you learn of a failure only when it ends. " +
		"A long check belongs in baton: " + how + " " +
		"A quick check (the tests of what you changed, by name) runs in the foreground, in place. " +
		"If this one must run as a background task all the same, make the same call again: it goes through."
}

// BackgroundCheckSaid is the line the human sees when baton questions a check started as a background
// task (BackgroundCheck).
func (w Words) BackgroundCheckSaid(command string) string {
	job := "baton verify"
	if !w.Git {
		job = "baton await"
	}
	return "baton: pointed a check started in the background (" + command + ") to " + job + ", once; the same call again runs it"
}

// WaitLoop is why baton refuses, once, a shell command of the model's own that only waits by polling
// (lane.IsWaitLoop), command naming it: started as a background task (timeout 0), or in the foreground with a
// timeout over the 2 minutes Claude Code gives a foreground command by default (timeout, so it holds the
// turn while it polls). Claude Code wakes the model when a background task ends, and that result names the
// task's output file, so nothing need poll for it; a wait on something outside the project belongs in baton
// await. In a real run of a 74-phase campaign one such loop polled for a line that never came for 22 hours.
// The same call again goes through, so this is information, never a wall.
func (w Words) WaitLoop(command string, timeout time.Duration) string {
	what := "[baton] Not run yet: this starts a loop that only waits (" + command + ") as a background task of your own. " +
		"A loop that waits for something that never comes runs as long as the session: in a real run, one polled for 22 hours. "
	if timeout > 0 {
		what = "[baton] Not run yet: this waits in a loop (" + command + ") with a timeout of " + Spell(timeout) + ", holding your turn while it polls. "
	}
	return what +
		"Claude Code wakes you when a background task ends, and its result names the task's output file, so wait for that wake: " +
		"keep working on what does not depend on it, and with nothing left to do, end your turn. " +
		"Never poll another task's output. " +
		"To wait on something outside the project (a CI run, a deploy, a server coming up), " + w.jobCall("await", "<a command that ends when it is ready>") +
		" runs it, records it and gives you the result when it ends. " +
		"If this loop must run all the same, make the same call again: it goes through."
}

// WaitLoopSaid is the line the human sees when baton questions a wait loop (WaitLoop): timeout is 0 for
// one started in the background.
func (w Words) WaitLoopSaid(command string, timeout time.Duration) string {
	where := "started in the background"
	if timeout > 0 {
		where = "in the foreground with a timeout of " + Spell(timeout)
	}
	return "baton: questioned a loop that only waits, " + where + " (" + command + "), once; the same call again runs it"
}
