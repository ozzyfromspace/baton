package decide

import "fmt"

// CheckpointOnResume is what the model must do first when the human resumed the run after the
// conversation grew while it was paused: record where the phase stands, so the phase goes on from a
// compacted context and a brief that carries those notes, rather than from everything said meanwhile.
func CheckpointOnResume(phase string) string {
	return fmt.Sprintf("The conversation grew while baton was paused, so baton compacts it before %s goes on. "+
		"First record where %s stands: run `baton checkpoint --notes \"<what is done, what is left, and anything from the paused conversation the rest of the phase needs>\"`, then end your turn. "+
		"baton compacts the context, and you continue %s from your notes.", phase, phase, phase)
}

// FreshStart opens the brief of a conversation that started without its phase's context: the human ran
// /clear, a rewind went back past baton's brief, or baton started a fresh session after a crash. running
// says whose session it is, and which plan it runs.
func FreshStart(running string) string {
	return "This conversation started without the phase's context (a /clear, a rewind past baton's brief, or a fresh session after a crash), " +
		"so baton gives you the phase's brief again: it goes on from the plan, the notes and the repository. " + running
}

// FreshNow is the brief's "Now:" line after a fresh start (title: "P3 — Title"). Some of the phase may be
// done already, and nothing in the conversation says so: the model checks before it redoes anything.
func (w Words) FreshNow(title string) string {
	check := "the files the phase is about, and any notes it left below"
	if w.Git {
		check = "`git log`, `git status`, and any notes the phase left below"
	}
	return fmt.Sprintf("Now: continue %s. Some of it may be done already: check what is (%s) before you redo anything, then go on from there.", title, check)
}
