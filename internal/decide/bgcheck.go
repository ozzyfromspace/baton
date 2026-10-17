package decide

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
