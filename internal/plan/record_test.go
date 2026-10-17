package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordDoc is a plan whose standing rules say rule, among other rules.
func recordDoc(rule string) []byte {
	return []byte("# Demo\n\n## Standing rules\n\n- Be kind.\n" + rule + "\n- **Records:** no shared progress file.\n\n" +
		"## P0 — First\n\nAfter: none\n\nDo a.\n\n## P1 — Second\n\nDo b. Record: P1's notes go nowhere.\n\n## Verification\n\nAll green.\n")
}

// A plan names its record file on a Record: line of its standing rules, in any of the ways a rule is
// written, relative to the project's root: Build keeps it as the plan's Record. Rules that only speak of
// records, and a Record: line outside the standing rules, name none.
func TestTheStandingRulesNameTheRecordFile(t *testing.T) {
	for _, c := range []struct {
		name, rule, want string
	}{
		{"a plain line", "Record: docs/record.md", "docs/record.md"},
		{"a bullet", "- Record: RECORD.md", "RECORD.md"},
		{"bold, as a code span", "- **Record:** `docs/record.md`.", "docs/record.md"},
		{"its label in another case", "record: notes/RUN.md", "notes/RUN.md"},
		{"a path written loosely", "Record: ./docs//record.md", "docs/record.md"},
		{"a path climbing back in", "Record: docs/../RECORD.md", "RECORD.md"},
		{"the same file twice", "Record: RECORD.md\n- Record: `./RECORD.md`", "RECORD.md"},
		{"no Record: line", "- Commit often.", ""},
		{"prose about the record", "- **Record:** each phase's notes, in baton done.", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := recordDoc(c.rule)
			p, err := Build("/plans/demo.md", doc, Suggest(doc))
			if err != nil {
				t.Fatal(err)
			}
			if p.Record != c.want {
				t.Errorf("Record = %q, want %q", p.Record, c.want)
			}
			if only, _ := p.Only("P1"); only.Record != p.Record {
				t.Errorf("a phase agent's plan has the record %q, the run's %q", only.Record, p.Record)
			}
		})
	}
}

// Build refuses, saying why, a record file outside the project, one that names a directory, one inside
// git's or baton's own files, and two record files; with the project's root, a directory there too.
func TestBuildRefusesARecordFileItCannotWrite(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	for _, c := range []struct {
		name, rule, root, want string
	}{
		{"an absolute path", "Record: /tmp/record.md", "", "is outside the project"},
		{"a home path", "Record: ~/record.md", "", "is outside the project"},
		{"a path climbing out", "Record: ../record.md", "", "climbs out of the project"},
		{"a path climbing out further in", "Record: docs/../../record.md", "", "climbs out of the project"},
		{"a trailing slash", "Record: docs/", "", "names a directory"},
		{"the root", "Record: `.`", "", "names the project's root"},
		{"git's files", "Record: .git/record.md", "", "inside .git"},
		{"baton's files", "Record: .baton/record.md", "", "inside .baton"},
		{"two files", "Record: RECORD.md\n- Record: docs/record.md", "", "name two record files"},
		{"a directory in the project", "Record: docs", root, "names a directory in the project"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := recordDoc(c.rule)
			spec := Suggest(doc)
			spec.Root = c.root
			_, err := Build("/plans/demo.md", doc, spec)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "relative to the project's root") && c.name != "two files" {
				t.Errorf("err = %v, want one saying %q and how to write it", err, c.want)
			}
		})
	}
	doc := recordDoc("Record: docs")
	if p, err := Build("/plans/demo.md", doc, Suggest(doc)); err != nil || p.Record != "docs" {
		t.Errorf("with no root, a path's form alone is checked: %+v, %v", p.Record, err)
	}
	spec := Suggest(recordDoc("Record: docs/record.md"))
	spec.Root = root
	if p, err := Build("/plans/demo.md", recordDoc("Record: docs/record.md"), spec); err != nil || p.Record != "docs/record.md" {
		t.Errorf("a file in a directory of the project: %q, %v", p.Record, err)
	}
}

// A plan file edited mid-run may name another record file, or none: baton follows it, and refuses a
// record it cannot write as it refuses any change that does not fit.
func TestRecheckFollowsTheRecordLine(t *testing.T) {
	doc := recordDoc("Record: RECORD.md")
	p, err := Build("/plans/demo.md", doc, Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, rule, want, err string
	}{
		{"another file", "Record: docs/record.md", "docs/record.md", ""},
		{"none", "- Commit often.", "", ""},
		{"one outside the project", "Record: ../record.md", "RECORD.md", "climbs out of the project"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, changed, err := Follow(p, recordDoc(c.rule), []string{"P1"})
			if !changed {
				t.Fatal("the text changed")
			}
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("err = %v, want %q", err, c.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got.Record != c.want {
				t.Errorf("Record = %q, want %q", got.Record, c.want)
			}
		})
	}
	if got, _, err := Follow(p, recordDoc("Record: docs/record.md"), nil); err != nil || got.Record != "docs/record.md" {
		t.Errorf("with every phase done: %q, %v", got.Record, err)
	}
}
