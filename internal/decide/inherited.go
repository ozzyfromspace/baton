package decide

import "fmt"

// A session that only inherited baton's environment has BATON_HOST=1 with no baton host above it
// (package hostcheck): Claude Code moved the conversation to its background service, say, under a new
// session id. baton leaves it alone, and these are the words that say so, and the words for carrying its
// run on once it is hosted again.

// InheritedStatus is how `baton version` and `baton status` name such a session.
const InheritedStatus = "not hosted (inherited)"

// Inherited is what baton tells the human, once, in a session (its id) that only inherited baton's
// settings: nothing of baton's works there, and how to give it back to baton.
func Inherited(session string) string {
	if session == "" {
		session = "<session id>"
	}
	return "baton: this session is not hosted by baton: it inherited baton's settings (for example when Claude Code moved it to the background), " +
		"so nothing here compacts it, starts phase agents or runs its plan. To give it back to baton, run this in a terminal: baton --resume " + session
}

// RunCarried is what baton says as a session with no plan of its own carries on the run of plan title that
// another session left with no live host (from, its sessions): phases done of all, the phase in hand
// (at, "" for none).
func RunCarried(title, from string, done, all int, at string) string {
	where := "with no phase in hand"
	if at != "" {
		where = "at " + at
	}
	return fmt.Sprintf("baton: carried on the run of %q that session %s left with no baton host: %d of %d phases done, %s. "+
		"This session goes on where it stood, with its notes and decisions.", title, from, done, all, where)
}

// CarriedOn tells the model what to do once its session carried a run on that has nothing to resume:
// go on with phase, the one in hand ("" for none), or else end its turn for what comes next.
func CarriedOn(phase string) string {
	if phase != "" {
		return "baton: go on with " + phase + " where it stood: `baton status` shows the run, with the notes of the phases before it."
	}
	return "baton: end your turn now: baton gives you what comes next with a fresh brief, or wakes you when there is something to do."
}
