package hooks

import "github.com/ozzyfromspace/baton/internal/state"

// wrapOf is the human's wrap of the run (/baton wrap, state.Wrap) that a brief in run s carries, st being s's
// state: the lead's own, or, for a phase agent's sub-run, the lead's run's, read without its lock
// (state.Store.LeadWrap). A fresh agent hears of the wrap in its brief, since the message that told of it went
// to the agent it replaced, or to none; nil while the run is not wrapping up.
func wrapOf(s *state.Store, st state.State) *state.Wrap {
	if s.Worker() == "" {
		return st.Wrap
	}
	return s.LeadWrap()
}
