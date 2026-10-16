package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan attached from the session's plan-mode file, which Claude Code attaches in full to the
// conversation after every compaction while it exists (spike 38), is moved out of the way as baton
// attaches it (state.Aside), and the run follows it there.

// lookAside reads, before the run's lock, what moving file, the plan the session attaches in run s (whose
// state st is), out of Claude Code's way needs: the session's transcript is the one its hooks marked last
// (Runtime.Proven). A worker's sub-run, a shell, and a session no hook of baton's has marked move nothing.
func lookAside(io IO, s *state.Store, st state.State, file string) state.Aside {
	if s.Worker() != "" || io.Env("CLAUDE_CODE_SESSION_ID") == "" || st.Run.Proven == nil {
		return state.Aside{File: file}
	}
	return s.ProjectOf().LookAside(s.Ref, file, st.Run.Proven.Path)
}

// movedAside logs, and says, what moving the plan file file out of Claude Code's way as `baton attach`
// attached it did (state.Aside.Move): plan_moved when it moved to pl's file, or plan_not_moved with why for
// the session's plan-mode file left where it is.
func movedAside(io IO, s *state.Store, pl plan.Plan, a state.Aside, file string, moved bool, err error) {
	switch {
	case moved:
		s.Event("plan_moved", map[string]any{"from": pl.Origin, "to": pl.File, "by": "attach"})
		fmt.Fprintln(io.Out, decide.PlanMoved(pl.Origin, pl.File))
	case err != nil:
		s.Event("plan_not_moved", map[string]any{"plan": file, "why": err.Error(), "by": "attach"})
	case a.PlanMode(file) && a.UsedBy != "":
		s.Event("plan_not_moved", map[string]any{"plan": file, "why": "another live run's plan", "run": a.UsedBy, "by": "attach"})
	}
}

// gone reports that nothing is at path: a plan file named by the path baton moved it from is the run's own
// while Claude Code has written no other plan there (cmdRun).
func gone(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}
