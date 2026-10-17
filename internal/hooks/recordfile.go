package hooks

import (
	"runtime"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// recordEdit refuses an edit of the plan's record file (plan.Plan.Record) by any model of the run, the
// lead's, a phase agent's or an agent either of them started, with the edit tools: baton alone writes the
// file, appending each phase's record and committing it (loop/record.go), so a model's edit would only
// leave it dirty, which holds every record after it. The file is found in the working tree of the loop the
// call is in, and in the lead's checkout. While no plan is attached it is the project's like any other.
func (h *handlers) recordEdit(c Context) (Result, bool) {
	tool := str(c.Input, "tool_name")
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return Result{}, false
	}
	s, err := h.d.Open(c)
	if err != nil || s == nil {
		return Result{}, false
	}
	run := s
	if s.Worker() != "" {
		if run, err = s.ParentRun(); err != nil {
			return Result{}, false
		}
	}
	pl, err := run.LoadPlan()
	if err != nil || pl.Record == "" {
		return Result{}, false
	}
	if st, err := run.Load(); err != nil || st.Mode == state.ModeIdle {
		return Result{}, false
	}
	path := ""
	for _, root := range []string{s.Root, s.Checkout} {
		if p := editedPath(c.Input, root); p != "" && samePath(p, pl.Record) {
			path = p
			break
		}
	}
	if path == "" {
		return Result{}, false
	}
	fields := map[string]any{"tool": tool, "path": path}
	if from := s.Worker(); from != "" {
		fields["phase"] = from // a phase agent's call, or one of its own agents'
	}
	run.Event("record_edit_refused", fields)
	res := permission("deny", decide.RecordOwned(path))
	res.Output["systemMessage"] = decide.RecordEditRefused(path)
	return res, true
}

// samePath reports whether two paths relative to the same root name the same file: ignoring case where
// the file system usually does (macOS, Windows).
func samePath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
