package decide

import (
	"fmt"
	"time"
)

// A proposal the human chose to hold (Wait for me), or whose question went away unanswered, holds its
// phase for them, but not for long: once nobody has written for loop's Timing.HoldLapse, baton lifts the
// hold. A proposal baton could have let go ahead at a deadline goes ahead then, or a more reversible way
// to the same end; one only the human could let through (state.Decision.Untimed) is answered Not now, and
// the model does it only if it is the safe kind (NotNowRule). These are the words the human and the model
// read about it.

// HoldLapse is how long a hold for the human lasts with no word from them (loop's Timing.HoldLapse): as
// long as the longest a proposal may wait for them (ProposalWaitMax).
const HoldLapse = ProposalWaitMax

// Holding is what baton says to the human as it holds the lead's proposal id for them, at their word
// (Wait for me): how long the hold lasts with no word from them (lapse), and what baton does then.
// outward says it is one only they could let through.
func Holding(id string, lapse time.Duration, outward bool) string {
	then := "baton lifts the hold and Claude goes ahead with it, or a more reversible way"
	if outward {
		then = "baton answers Not now: Claude does it only if it is safely reversible and stays in the project"
	}
	return fmt.Sprintf("baton: holding %s for you — answer in the session; with no word from you in %s, %s", id, Spell(lapse), then)
}

// NotNowRule is what the model does with a proposal baton answered Not now for the human: only the kind
// that is safely reversible and stays inside the project, noted; nothing irreversible, nothing that
// reaches outside.
func NotNowRule() string {
	return "Do it only if it is safely reversible and stays inside the project (pushing your own branch, a draft pull request, deleting files you generated), " +
		"and then record it: baton note \"<what you did>\" --undo \"<how to reverse it>\". If it is irreversible or reaches outside the project " +
		"(publishing, deploying, paying, sending, force-pushing, deleting data others rely on), do not: leave it undone for the human, " +
		"and carry on with everything that does not need it."
}

// HoldLapsed is what baton says to the human (say) and tells the model (tell) when the hold on the lead's
// proposal id (to do what, undone with undo) lapsed, nobody having written for after. outward says it was
// one only the human could let through, answered Not now; otherwise it goes ahead, or a more reversible
// way does. last says it was the last decision phase may make without the human before they review them;
// otherwise the model carries on with phase.
func HoldLapsed(id, what, undo, phase string, after time.Duration, outward, last bool) (say, tell string) {
	return holdLapsed("", id, what, undo, Named(phase), after, outward, last)
}

// RelayHoldLapsed is HoldLapsed for the proposal id of the phase agent on phase: say is for the human, in
// the lead's session, and tell for the agent.
func RelayHoldLapsed(id, what, undo, phase string, after time.Duration, outward, last bool) (say, tell string) {
	return holdLapsed(phase, id, what, undo, phase, after, outward, last)
}

// holdLapsed words a lapsed hold on proposal id of the agent on agent, or the lead's ("").
func holdLapsed(agent, id, what, undo, phase string, after time.Duration, outward, last bool) (say, tell string) {
	whose, who := id, "Claude"
	if agent != "" {
		whose, who = "the proposal "+id+" of the agent on "+agent, "the agent"
	}
	nobody := fmt.Sprintf("nobody answered in %s", Spell(after))
	held := fmt.Sprintf("[baton] Your proposal %s (%s) was held for the human, and nobody has written for %s, so baton ", id, clause(what), Spell(after))
	if outward {
		say = fmt.Sprintf("baton: %s, so baton answered Not now for you on %s → %s does it only if it is safely reversible and stays in the project; "+
			"otherwise it is left for you — %s", nobody, whose, who, clause(what))
		tell = held + "answered Not now for them: the run does not wait on them any longer. " + NotNowRule() + " Then carry on with " + phase + "."
		return say, tell
	}
	say = fmt.Sprintf("baton: %s, so baton lifted the hold on %s → %s goes ahead with it, or a more reversible way — %s", nobody, whose, who, clause(what))
	tell = held + "lifted the hold for them: the run does not wait on them any longer. Do what you proposed now, or a more reversible way to the same end, " +
		"and if you take another way, record it: baton note \"<what you did>\" --undo \"<how to reverse it>\". " +
		"If they later ask, what you proposed is undone with: " + undo + "."
	if last {
		return say, tell + " That was the last decision baton lets " + phase + " make without the human: once it is done, end your turn, and baton stops " +
			phase + " for them to review the decisions."
	}
	return say, tell + " Then carry on with " + phase + "."
}

// HoldLifted is what baton says to the human (say) and tells the model (tell) when the run's hold for them
// (reason), with no proposal of the model's behind it, lapsed, nobody having written for after: phase goes
// on.
func HoldLifted(reason, phase string, after time.Duration) (say, tell string) {
	say = fmt.Sprintf("baton: nobody answered in %s, so baton lifted the hold — %s", Spell(after), clause(reason))
	tell = fmt.Sprintf("[baton] The run was held for the human (%s), and nobody has written for %s, so baton lifted the hold: "+
		"the run does not wait on them any longer. Carry on with %s.", clause(reason), Spell(after), Named(phase))
	return say, tell
}
