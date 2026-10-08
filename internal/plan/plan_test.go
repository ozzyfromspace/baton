package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ids(s Spec) []string {
	var out []string
	for _, p := range s.Phases {
		out = append(out, p.ID)
	}
	return out
}

func TestSuggestRecognizesEachNotation(t *testing.T) {
	cases := []struct {
		file, title, rules, end string
		ids                     []string
		titles                  []string
	}{
		{"headings-emdash.md", "The widget campaign", "## Standing rules for the run", "## Verification",
			[]string{"P0", "P1", "R1"}, []string{"The overlay", "The one-liners", "The roster can grow"}},
		{"headings-colon.md", "Release plan", "", "## Risks",
			[]string{"P22", "P23"}, []string{"Polish", "Final QC, docs, wrap-up"}},
		{"bold-bullets.md", "Bootstrap plan", "", "## Verification",
			[]string{"P0", "P1", "P2"}, []string{"Bootstrap", "Verification round 2", "Go skeleton"}},
		{"table.md", "Dress rehearsal", "", "",
			[]string{"P1", "P2", "P3"}, []string{"Seed the tenants", "Walk the personas", "Restore"}},
		{"phase-letters.md", "Migration", "", "",
			[]string{"Phase-A", "Phase-B", "Phase-C"}, []string{"Inventory", "Move", "Phase C"}},
		// Phase headings, with a context table whose rows also start with ids (S1…S4), and a bold bullet
		// and a table row that look like phase ids too: only the headings are phases.
		{"headings-with-table.md", "baton v0.2 — graduated escalation", "## Standing rules", "## Verification",
			[]string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7"}, []string{"Evidence and the settled design", "Dialog safety"}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			doc := fixture(t, c.file)
			s := Suggest(doc)
			if s.Title != c.title || s.RulesAnchor != c.rules || s.EndAnchor != c.end {
				t.Errorf("title/rules/end = %q / %q / %q", s.Title, s.RulesAnchor, s.EndAnchor)
			}
			if !reflect.DeepEqual(ids(s), c.ids) {
				t.Fatalf("ids = %v, want %v", ids(s), c.ids)
			}
			for i, want := range c.titles {
				if s.Phases[i].Title != want {
					t.Errorf("phase %d title = %q, want %q", i, s.Phases[i].Title, want)
				}
			}
			// A suggestion must always validate against its own document.
			if _, err := Build(c.file, doc, s); err != nil {
				t.Errorf("suggestion does not build: %v", err)
			}
		})
	}
}

func TestBuildRejectsBadSpecs(t *testing.T) {
	doc := fixture(t, "headings-emdash.md")
	good := Suggest(doc)
	mutate := func(f func(*Spec)) Spec {
		s := good
		s.Phases = append([]Phase(nil), good.Phases...)
		f(&s)
		return s
	}
	cases := map[string]struct {
		spec Spec
		want string
	}{
		"missing anchor":   {mutate(func(s *Spec) { s.Phases[1].Anchor = "## P1 — Nope" }), "does not occur"},
		"ambiguous anchor": {mutate(func(s *Spec) { s.Phases[0].Anchor = "## " }), "occurs"},
		"out of order":     {mutate(func(s *Spec) { s.Phases[0], s.Phases[1] = s.Phases[1], s.Phases[0] }), "document order"},
		"duplicate id":     {mutate(func(s *Spec) { s.Phases[1].ID = "P0" }), "duplicate id"},
		"bad id":           {mutate(func(s *Spec) { s.Phases[0].ID = "P 0" }), "id must be"},
		"empty title":      {mutate(func(s *Spec) { s.Phases[2].Title = " " }), "title is empty"},
		"no phases":        {Spec{Title: "x"}, "no phases"},
		"end before last":  {mutate(func(s *Spec) { s.EndAnchor = "## Context" }), "end_anchor must come after"},
	}
	for name, c := range cases {
		if _, err := Build("plan.md", doc, c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
	}
}

func TestSectionsAndRules(t *testing.T) {
	doc := fixture(t, "headings-emdash.md")
	p, err := Build("plan.md", doc, Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	s0, _ := p.Section(doc, 0)
	if !strings.HasPrefix(s0, "## P0 — The overlay") || strings.Contains(s0, "P1") {
		t.Errorf("section 0 = %q", s0)
	}
	last, _ := p.Section(doc, 2)
	if !strings.Contains(last, "Add people") || strings.Contains(last, "Verification") {
		t.Errorf("last section must stop at the end anchor: %q", last)
	}
	rules := p.Rules(doc)
	if !strings.Contains(rules, "Stop only for a decision") || strings.Contains(rules, "overlay") {
		t.Errorf("rules = %q", rules)
	}
	if p.Index("R1") != 2 || p.Index("nope") != -1 {
		t.Error("Index")
	}
	if len(p.SHA256) != 64 || p.Version != SchemaVersion {
		t.Error("identity fields not set")
	}
}

func TestSectionAfterEditReportsTheMissingAnchor(t *testing.T) {
	doc := fixture(t, "headings-emdash.md")
	p, _ := Build("plan.md", doc, Suggest(doc))
	edited := []byte(strings.Replace(string(doc), "## P1 — The one-liners", "## P1 — Renamed", 1))
	if _, err := p.Section(edited, 1); err == nil || !strings.Contains(err.Error(), "edited after attach") {
		t.Fatalf("err = %v", err)
	}
}

func TestShaped(t *testing.T) {
	for _, c := range []struct {
		doc  string
		want bool
	}{
		{"# T\n\n## P0 — A\nx\n\n## P1 — B\ny\n", true},
		{"# T\n\n### Phase 1: A\nx\n\n### Phase 2: B\ny\n", true},
		{"# T\n\n## P0 — Only one\nx\n", false},
		{"# T\n\n- **P0 A:** x\n- **P1 B:** y\n", false},
		{"# Fix the login bug\n\nChange the check in auth.go.\n", false},
	} {
		if got := Shaped(Suggest([]byte(c.doc))); got != c.want {
			t.Errorf("%q: %v, want %v", c.doc, got, c.want)
		}
	}
}

func TestRecheckFollowsEditsThatKeepThePhases(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.md")
	doc := "# T\n\n## Standing rules\nr\n\n## P0 — A\nx\n\n## P1 — B\ny\n\n## P2 — C\nz\n\n## Verification\nv\n"
	os.WriteFile(file, []byte(doc), 0o644)
	p, err := Build(file, []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := Recheck(p, []string{"P1", "P2"}); changed || err != nil {
		t.Fatalf("unchanged file: %v %v", changed, err)
	}
	// P1's instructions edited: every phase still there, so baton follows the new text.
	os.WriteFile(file, []byte(strings.Replace(doc, "y\n", "y, and also w\n", 1)), 0o644)
	adopted, changed, err := Recheck(p, []string{"P1", "P2"})
	if !changed || err != nil || adopted.SHA256 == p.SHA256 || adopted.SHA256 != adopted.SHA() {
		t.Fatalf("edited: %v %v %s", changed, err, adopted.SHA256)
	}
	// A done phase's heading may go; one still to run may not.
	os.WriteFile(file, []byte(strings.Replace(doc, "## P0 — A", "## P0 — A (done)", 1)), 0o644)
	if _, _, err := Recheck(p, []string{"P1", "P2"}); err != nil {
		t.Errorf("a done phase renamed: %v", err)
	}
	os.WriteFile(file, []byte("# Another plan\n\n## P0 — Other\nq\n"), 0o644)
	if _, changed, err := Recheck(p, []string{"P1", "P2"}); !changed || err == nil || !strings.Contains(err.Error(), "## P1 — B") {
		t.Errorf("replaced by another plan: %v %v", changed, err)
	}
	os.Remove(file)
	if _, changed, err := Recheck(p, []string{"P1"}); !changed || err == nil {
		t.Errorf("file gone: %v %v", changed, err)
	}
}

// A phase the human adds to the plan file joins the run, in document order; lines that merely look like
// phases do not, and neither does anything in a plan whose phases are not headings.
func TestRecheckAdoptsAddedPhases(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.md")
	doc := "# T\n\n## P0 — A\nx\n\n## P1 — B\ny\n\n## Verification\nv\n"
	os.WriteFile(file, []byte(doc), 0o644)
	p, err := Build(file, []byte(doc), Suggest([]byte(doc)))
	if err != nil || p.EndAnchor != "## Verification" {
		t.Fatal(err, p.EndAnchor)
	}
	phases := func(p Plan) string {
		var out []string
		for _, ph := range p.Phases {
			out = append(out, ph.ID+" "+ph.Title)
		}
		return strings.Join(out, ", ")
	}
	for _, tc := range []struct {
		name, doc, phases, end string
		remaining              []string
	}{
		{"before the end marker", strings.Replace(doc, "## Verification", "## P2 — C\nz\n\n### S1: a spike, not a phase\n\n## Verification", 1),
			"P0 A, P1 B, P2 C", "## Verification", []string{"P1"}},
		{"after the end marker", doc + "\n## P2 — C\nz\n", "P0 A, P1 B, P2 C", "", []string{"P1"}},
		{"to a finished plan", strings.Replace(doc, "## Verification", "## P2 — C\nz\n\n## P3 — D\nw\n\n## Verification", 1),
			"P0 A, P1 B, P2 C, P3 D", "## Verification", nil},
		{"between phases still to run", strings.Replace(doc, "## P1 — B", "## P0b — A2\nq\n\n## P1 — B", 1), "P0 A, P0b A2, P1 B", "## Verification", []string{"P0", "P1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.WriteFile(file, []byte(tc.doc), 0o644)
			adopted, changed, err := Recheck(p, tc.remaining)
			if !changed || err != nil || phases(adopted) != tc.phases || adopted.EndAnchor != tc.end || adopted.SHA256 != adopted.SHA() {
				t.Fatalf("changed %v err %v: phases %q end %q", changed, err, phases(adopted), adopted.EndAnchor)
			}
		})
	}

	bullets := "# T\n\n- **P0 A:** x\n- **P1 B:** y\n"
	os.WriteFile(file, []byte(bullets), 0o644)
	pb, err := Build(file, []byte(bullets), Suggest([]byte(bullets)))
	if err != nil {
		t.Fatal(err)
	}
	if added := Added(pb, []byte(bullets+"\n## P2 — C\nz\n")); len(added) != 0 {
		t.Errorf("a plan of bullets gained %v", added)
	}
}
