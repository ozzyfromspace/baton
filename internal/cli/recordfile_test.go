package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// `baton attach` keeps the record file a plan's standing rules name, and refuses, saying why, one that
// names a directory of the project.
func TestAttachChecksTheRecordFile(t *testing.T) {
	s, _ := newSession(t)
	root := filepath.Dir(s.env["BATON_DIR"])
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	doc := func(record string) string {
		file := filepath.Join(t.TempDir(), "plan.md")
		os.WriteFile(file, []byte("# Demo\n\n## Standing rules\n\n- Record: `"+record+"`\n\n## P0 — First\n\nDo a.\n\n## P1 — Second\n\nDo b.\n"), 0o644)
		return file
	}
	s.mustFail("names a directory in the project", "attach", doc("docs"), "--suggested")
	s.must("", "attach", doc("docs/RECORD.md"), "--suggested")
	pl, err := s.store().LoadPlan()
	if err != nil || pl.Record != "docs/RECORD.md" {
		t.Errorf("the plan's record is %q (%v)", pl.Record, err)
	}
}
