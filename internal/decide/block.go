package decide

import (
	"fmt"
	"time"
)

// A block (baton blocked) is the model's word that every way forward is irreversible or needs a secret
// only the human holds. It stops its phase for the human, but not for long: once nobody has answered for
// loop's Timing.BlockLook, baton lifts it, and the model looks again for a safe way on that drops no work,
// which it proposes (baton propose) so that the human may still say no by its deadline. If it finds none
// and blocks again, the same happens again. These are the words the human and the model read about it.

// BlockLook is how long a block waits for the human with no word from them (loop's Timing.BlockLook).
const BlockLook = 30 * time.Minute

// LookAgain is the answer to a block that lifts it for the model to look again: the one baton gives when
// nobody answers.
const LookAgain = "Look again"

// BlockedOptions are the answers to the lead's block. Look again is third, where every question of
// baton's keeps its default (EscalationOptions).
var BlockedOptions = []Option{
	{"Continue", "I've dealt with it; carry on with the plan"},
	{"Pause baton", "I'll take it from here"},
	{LookAgain, fmt.Sprintf("Claude looks for a safe way around it and proposes it first; baton picks this if nobody answers in %s", Spell(BlockLook))},
}

// LookingAgain is what baton says to the human (say) and tells the model (tell) when the block on phase
// (reason) is lifted for the model to look again: nobody answered it for after (auto), or the human chose
// Look again. agent is the phase of the phase agent whose block it was ("" for the lead's); wait is how
// long a proposal waits for the human unless the model sets its own (escalation_timeout).
func LookingAgain(agent, reason, phase string, after, wait time.Duration, auto bool) (say, tell string) {
	whose, who := "the block on "+Named(phase), "Claude"
	if agent != "" {
		whose, who = "the block of the agent on "+agent, "the agent"
	}
	if auto {
		say = fmt.Sprintf("baton: nobody answered %s in %s, so baton lifted it → %s looks again for a safe way on, and proposes it — %s",
			whose, Spell(after), who, clause(reason))
		tell = fmt.Sprintf("[baton] You were blocked on %s (%s), and nobody has answered for %s, so baton lifted the block for the human: "+
			"the run does not wait on them any longer.", Named(phase), clause(reason), Spell(after))
	} else {
		say = fmt.Sprintf("baton: %s is lifted, as you chose → %s looks again for a safe way on, and proposes it — %s", whose, who, clause(reason))
		tell = fmt.Sprintf("[baton] The human chose Look again on your block on %s (%s).", Named(phase), clause(reason))
	}
	return say, tell + " " + LookAgainRule(wait)
}

// LookAgainRule is what the model does once its block is lifted for it to look again: a safe way on that
// completes the assignment and drops no work, the independent parts first, and a way around what blocked
// it proposed before it is taken, which goes ahead if nobody answers by its deadline (wait, unless the
// model sets its own).
func LookAgainRule(wait time.Duration) string {
	return "Look again for a safe, independent way to continue that completes the assignment without dropping work: " +
		"first the parts that do not need what blocked you, then a reversible way around it. Nothing irreversible. " +
		"Propose that way before you take it: baton propose \"<what you will do>\" --because \"<what blocked you>\" --undo \"<how to reverse it>\". " +
		fmt.Sprintf("The human has until its deadline (your --wait, or %s) to say no, and if nobody answers, you go ahead. ", Spell(wait)) +
		"Only if every way forward is still irreversible, or needs a secret only the human holds, block again with what you tried: " +
		"baton blocked \"<why>\" --tried \"<what you tried>\"."
}
