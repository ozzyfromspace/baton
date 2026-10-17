package hooks

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// withRecord has the run's plan name the record file docs/RECORD.md, as a Record: line in its standing
// rules would.
func withRecord(f *fixture) {
	f.t.Helper()
	pl, err := f.store.LoadPlan()
	if err != nil {
		f.t.Fatal(err)
	}
	pl.Record = "docs/RECORD.md"
	if err := f.store.SavePlan(pl); err != nil {
		f.t.Fatal(err)
	}
}

// No model of the run edits the plan's record file, with any edit tool: the lead's, a phase agent's in
// its worktree or the lead's checkout, by an absolute path or one relative to where it works. Each refusal
// says why, and is logged in the lead's run. Other files go through, and so does the record file of a plan
// that names none.
func TestNoModelEditsTheRecordFile(t *testing.T) {
	w, lead, tree := newWorker(t)
	root := lead.store.Root
	withRecord(lead)
	for _, c := range []struct {
		name  string
		f     *fixture
		in    map[string]any
		path  string // the record file it is refused for, "" for a call let through
		phase string // the phase agent whose call it is, logged
	}{
		{"the lead's Edit", lead, edit("Edit", filepath.Join(root, "docs", "RECORD.md")), "docs/RECORD.md", ""},
		{"the lead's Write", lead, edit("Write", filepath.Join(root, "docs", "RECORD.md")), "docs/RECORD.md", ""},
		{"the lead's MultiEdit", lead, edit("MultiEdit", filepath.Join(root, "docs", "RECORD.md")), "docs/RECORD.md", ""},
		{"the lead's NotebookEdit", lead, edit("NotebookEdit", filepath.Join(root, "docs", "RECORD.md")), "docs/RECORD.md", ""},
		{"the lead's, by a relative path", lead, withCwd(edit("Write", "RECORD.md"), filepath.Join(root, "docs")), "docs/RECORD.md", ""},
		{"a phase agent's, in its worktree", w, edit("Edit", filepath.Join(tree, "docs", "RECORD.md")), "docs/RECORD.md", "P2"},
		{"a phase agent's, in the lead's checkout", w, edit("Write", filepath.Join(root, "docs", "RECORD.md")), "docs/RECORD.md", "P2"},
		{"the lead's, another file", lead, edit("Write", filepath.Join(root, "docs", "OTHER.md")), "", ""},
		{"a phase agent's, another file", w, edit("Edit", filepath.Join(tree, "main.go")), "", ""},
		{"the lead's Read of it", lead, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(root, "docs", "RECORD.md")}}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			hso, _ := c.f.fire("PreToolUse", c.in)["hookSpecificOutput"].(map[string]any)
			why, _ := hso["permissionDecisionReason"].(string)
			if c.path == "" {
				if strings.Contains(why, "record file") {
					t.Errorf("refused as the record file: %q", why)
				}
				return
			}
			if hso["permissionDecision"] != "deny" || why != decide.RecordOwned(c.path) {
				t.Errorf("said %v %q, want deny %q", hso["permissionDecision"], why, decide.RecordOwned(c.path))
			}
			fields := map[string]any{"tool": c.in["tool_name"], "path": c.path}
			if c.phase != "" {
				fields["phase"] = c.phase
			}
			if !lead.logged("record_edit_refused", fields) {
				t.Errorf("not logged %v:\n%s", fields, lead.eventLog())
			}
		})
	}
	if n := len(lead.eventsOf("record_edit_refused")); n != 7 {
		t.Errorf("%d refusals logged:\n%s", n, lead.eventLog())
	}
}

// A plan that names no record file, and a project with no plan, leave every file to the models.
func TestARecordFileIsTheModelsWithoutTheLine(t *testing.T) {
	for _, withPlan := range []bool{true, false} {
		f := newFixture(t, withPlan)
		path := filepath.Join(f.store.Root, "docs", "RECORD.md")
		if why := denied(f, edit("Write", path)); strings.Contains(why, "record file") {
			t.Errorf("with a plan %v: refused %q", withPlan, why)
		}
	}
	f := newFixture(t, true)
	withRecord(f)
	f.set(func(st *state.State) { *st = state.New() }) // the plan dropped: the run is idle
	if why := denied(f, edit("Write", filepath.Join(f.store.Root, "docs", "RECORD.md"))); strings.Contains(why, "record file") {
		t.Errorf("with no plan running: refused %q", why)
	}
}

// Refusing an edit of the record file fails open: a panic in it exits 0 and writes nothing.
func TestRefusingARecordEditFailsOpen(t *testing.T) {
	root := "" // the project's, as the case's fixture has it
	failsOpen(t, []failCase{
		{"the lead's edit", "PreToolUse", func(string) map[string]any { return edit("Write", filepath.Join(root, "docs", "RECORD.md")) },
			func(f *fixture) { withRecord(f); root = f.store.Root }},
		{"a phase agent's edit", "PreToolUse", func(tree string) map[string]any {
			return phaseAgent(edit("Write", filepath.Join(tree, "docs", "RECORD.md")), tree)
		}, withRecord},
	})
}

// withCwd is in, run in dir.
func withCwd(in map[string]any, dir string) map[string]any {
	in["cwd"] = dir
	return in
}
