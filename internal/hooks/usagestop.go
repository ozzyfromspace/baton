package hooks

import (
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// readLimit reads a transcript for the usage limit that ended its last turn (a variable, so a test can make
// it fail).
var readLimit = transcript.Limited

// usageStop adds to e, a turn that ended in a rate_limit error, which of the account's usage windows it
// was and when that resets, from the transcript at path (agent: a phase agent's own). Claude Code writes
// both there just before StopFailure runs, and its input says neither (docs/research/usage.md). Reading is
// best effort: whatever goes wrong, even a panic, leaves e as Claude Code reported it.
func usageStop(e *state.StopError, path string, agent bool) {
	defer func() { recover() }()
	if lim, _ := readLimit(path, agent); lim != nil {
		e.Window, e.Resets = state.WindowOf(lim.Kind), lim.Resets
	}
}
