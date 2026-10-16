package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/valve"
)

// ContextNotice is the heads-up the model is given as its context passes the notice line: where the context
// stands, that nothing is asked of it now, and how to make the room last. checkpoint is where baton asks for
// a checkpoint (0: never), autoAt where Claude Code compacts on its own.
func (w Words) ContextNotice(phase string, used, limit, checkpoint, autoAt int) string {
	next := fmt.Sprintf("Claude Code compacts the context on its own at about %s.", valve.Tokens(autoAt))
	if checkpoint > used {
		next = fmt.Sprintf("At %s baton asks you to checkpoint at your next safe point (a checkpoint %s).", valve.Tokens(checkpoint), w.checkpoints())
	}
	return fmt.Sprintf("[baton] Context heads-up; nothing is asked of you now. The context holds %s tokens, %.0f%% of its %s limit. "+
		"Keep working on %s, and make the room last: read the part of a file you need (a line range, a grep) rather than all of it, "+
		"and keep command output short. %s",
		valve.Tokens(used), 100*float64(used)/float64(limit), valve.Tokens(limit), Named(phase), next)
}

// ContextNoticeSay is the line that tells the human the model was given the heads-up.
func ContextNoticeSay(used, limit, checkpoint int) string {
	s := fmt.Sprintf("baton: context at %s of %s → a heads-up to the model", valve.Tokens(used), valve.Tokens(limit))
	if checkpoint > used {
		s += "; checkpoint at " + valve.Tokens(checkpoint)
	}
	return s
}
