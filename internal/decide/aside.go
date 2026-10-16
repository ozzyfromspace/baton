package decide

import (
	"fmt"
	"path/filepath"
)

// What baton says as it moves a plan out of Claude Code's way (state.Aside): Claude Code adds a session's
// plan-mode file in full to the context after every compaction for as long as it is there (spike 38), and
// baton's briefs already quote what each phase needs of the plan.

// PlanMoved says baton moved the plan file from, the session's plan-mode file, to to, and follows it
// there. The human reads it, and so does the model: briefs name the new path from here on.
func PlanMoved(from, to string) string {
	return fmt.Sprintf("baton: moved the plan to %s (it was %s): Claude Code adds its plan-mode file in full to the context after every compaction, "+
		"and baton's briefs quote what each phase needs. baton follows the plan there; edit it there.", to, filepath.Base(from))
}
