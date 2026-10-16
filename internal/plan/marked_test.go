package plan

import (
	"reflect"
	"strings"
	"testing"
)

// A phase's Replan: line sits beside its After: and Type: lines, in any order: `Replan: after` marks a
// phase whose findings should reshape the phases after it.
func TestParseHeadReplan(t *testing.T) {
	for _, c := range []struct {
		name, section string
		after         after
		typ, replan   string
		err           string
	}{
		{"after, then replan", "## P0 — Spike\n\nAfter: none\nReplan: after\n\nTry it.", after{found: true}, "", ReplanAfter, ""},
		{"replan alone", "## P0 — Spike\nReplan: after\n\nTry it.", after{}, "", ReplanAfter, ""},
		{"bold, a full stop, any case", "## P0 — Spike\n**Replan:** After.\n**After:** P1", after{found: true, ids: []string{"P1"}}, "", ReplanAfter, ""},
		{"all three, the After: line last", "## P5 — QA\nType: qa\nReplan: after\nAfter: P2, P4\n\nGoal.", after{found: true, ids: []string{"P2", "P4"}}, TypeQA, ReplanAfter, ""},
		{"too far down", "## P0 — Spike\nAfter: none\nGoal.\nReplan: after", after{found: true}, "", "", ""},
		{"no line", "## P0 — Spike\nAfter: none", after{found: true}, "", "", ""},
		{"another value", "## P0 — Spike\nReplan: before", after{}, "", "", `Replan: "before" is not something baton knows`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := parseHead(c.section)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(h.after, c.after) || h.typ != c.typ || h.replan != c.replan {
				t.Fatalf("got %+v, %v; want %+v %q %q", h, err, c.after, c.typ, c.replan)
			}
		})
	}
}

// A plan built from its text marks the phases with a Replan: after line; a replan that only adds an After:
// line under them changes nothing else of them.
func TestAMarkedPhase(t *testing.T) {
	doc := "# Demo\n\n## P0 — Spike\n\nAfter: none\nReplan: after\n\nTry it.\n\n## P1 — Build\n\nType: qa\nReplan: after\n\nBuild it.\n\n## P2 — Ship\n\nShip it.\n"
	p, err := Build("demo.md", []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Phases[0].Marked() || !p.Phases[1].Marked() || p.Phases[2].Marked() || !p.Phases[1].QA() {
		t.Fatalf("phases %+v", p.Phases)
	}
	if d := p.Deps("P1"); len(d) != 1 || d[0] != "P0" {
		t.Errorf("P1 after %v", d)
	}
	draft := strings.Replace(doc, "Replan: after\n\nBuild it.", "Replan: after\nAfter: none\n\nBuild it.", 1)
	next, bs := Redraft(p, []byte(doc), []byte(draft), map[string]bool{}, Limits{})
	if len(bs) != 0 || len(next.Deps("P1")) != 0 || !next.Phases[1].Marked() {
		t.Errorf("an After: line under the Type: and Replan: lines: breaches %+v, P1 after %v", bs, next.Deps("P1"))
	}
	if _, err := Build("demo.md", []byte(strings.Replace(doc, "Replan: after\n\nTry", "Replan: soon\n\nTry", 1)), Suggest([]byte(doc))); err == nil ||
		!strings.Contains(err.Error(), `phase 1 ("P0"): Replan: "soon"`) {
		t.Errorf("an unknown Replan: line: %v", err)
	}
}
