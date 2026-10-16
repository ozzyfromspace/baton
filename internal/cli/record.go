package cli

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// keepRecord writes the record of the plan an update of s replaced (state.Store.Record, taken under the
// run's lock), now that the lock is let go, and logs it: plan_archived, or plan_not_archived with a
// warning. It returns the folder, "" for none.
func keepRecord(io IO, s *state.Store, rec *state.Record) string {
	if rec == nil {
		return ""
	}
	dir, err := rec.Save()
	if err != nil {
		s.Event("plan_not_archived", map[string]any{"generation": rec.Generation, "why": err.Error()})
		fmt.Fprintf(io.Err, "baton: warning: could not keep the record of the plan it replaced: %v\n", err)
		return ""
	}
	s.Event("plan_archived", map[string]any{"generation": rec.Generation, "dir": dir})
	return dir
}
