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
