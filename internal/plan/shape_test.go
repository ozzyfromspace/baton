package plan

import (
	"fmt"
	"strings"
	"testing"
)

// shapeDoc writes a plan with one phase per entry of after, each entry its After: line's words ("" for no
// line, so the phase follows the one before it).
func shapeDoc(after ...string) []byte {
	var b strings.Builder
	b.WriteString("# Shapes\n\n## Standing rules\n\nBe careful.\n")
	for i, a := range after {
		fmt.Fprintf(&b, "\n## P%d — Phase %d\n\n", i, i)
		if a != "" {
			fmt.Fprintf(&b, "After: %s\n\n", a)
		}
		b.WriteString("Work.\n")
	}
	b.WriteString("\n## Verification\n\nAll green.\n")
	return []byte(b.String())
}

// A plan's shape is read from its text as baton reads a plan put to the human (Suggest, then Build): its
// phases, its longest chain and its width, and whether it is mostly one chain (at least ShapeMinPhases
// phases, the chain over ShapeChainShare of them).
func TestShape(t *testing.T) {
	none := "none"
	for _, c := range []struct {
		name   string
		after  []string
		chain  string
		width  int
		mostly bool
	}{
		{"one phase", []string{""}, "P0", 1, false},
		{"a chain of six", []string{"", "", "", "", "", ""}, "P0 P1 P2 P3 P4 P5", 1, true},
		{"a chain of five: too few to flag", []string{"", "", "", "", ""}, "P0 P1 P2 P3 P4", 1, false},
		{"wide", []string{none, none, none, none, none, none, none, none}, "P0", 8, false},
		{"wide, then all", []string{none, none, none, none, none, "all"}, "P0 P5", 5, false},
		{"after all, all the way", []string{none, "all", "all", "all", "all", "all"}, "P0 P1 P2 P3 P4 P5", 1, true},
		{"a chain of six in ten: not over the share", []string{"", "", "", "", "", "", none, none, none, none}, "P0 P1 P2 P3 P4 P5", 5, false},
		{"a chain of seven in ten", []string{"", "", "", "", "", "", "", none, none, none}, "P0 P1 P2 P3 P4 P5 P6", 4, true},
		{"this release's plan", []string{none, none, none, none, none, none, none, "P1, P2", "P7",
			"P0, P1, P2, P3, P4, P5, P6, P7, P8", "P0, P1, P2, P3, P4, P5, P6, P7, P8", "all"}, "P1 P7 P8 P9 P11", 7, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := shapeDoc(c.after...)
			p, err := Build("shapes.md", doc, Suggest(doc))
			if err != nil {
				t.Fatal(err)
			}
			s := p.Shape()
			if s.Phases != len(c.after) || strings.Join(s.Chain, " ") != c.chain || s.Width != c.width {
				t.Errorf("Shape() = %+v, want %d phases, chain %s, width %d", s, len(c.after), c.chain, c.width)
			}
			if got := s.MostlyChain(); got != c.mostly {
				t.Errorf("MostlyChain() = %v, want %v", got, c.mostly)
			}
		})
	}
	if s := (Plan{}).Shape(); s.Phases != 0 || len(s.Chain) != 0 || s.Width != 0 || s.QA != 0 || s.MostlyChain() {
		t.Errorf("no phases: %+v", s)
	}
}

// A plan's shape counts its QA phases, which the gate rules ask the model to place at real seams. (An
// entry's "\nType: qa" puts that line under its After: line.)
func TestShapeCountsQAPhases(t *testing.T) {
	for _, c := range []struct {
		name  string
		after []string
		qa    int
	}{
		{"none", []string{"", "none", "all"}, 0},
		{"one at the end", []string{"", "none", "all\nType: qa"}, 1},
		{"one per phase", []string{"", "P0\nType: qa", "P1", "P2\nType: qa"}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := shapeDoc(c.after...)
			p, err := Build("shapes.md", doc, Suggest(doc))
			if err != nil {
				t.Fatal(err)
			}
			if s := p.Shape(); s.QA != c.qa {
				t.Errorf("Shape().QA = %d, want %d (%+v)", s.QA, c.qa, s)
			}
		})
	}
}
