package hooks

import "github.com/ozzyfromspace/baton/internal/state"

// stop records what the Stop event reports. The decision table that may refuse the stop, queue a
// compaction or escalate is added with the boundary loop.
func (h *handlers) stop(c Context) (Result, error) {
	s, st, err := h.update(c, func(st *state.State, _ *state.Store) error {
		recordStop(st, c)
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	s.Event("stop", map[string]any{"background": len(st.Run.BusyBackground()), "crons": st.Run.Crons})
	return Result{}, nil
}
