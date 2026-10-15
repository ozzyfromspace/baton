package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// graphOf shows each phase with what it waits for: "P0[] P1[] P2[P1]".
func graphOf(p Plan) string {
	var out []string
	for _, ph := range p.Phases {
		out = append(out, ph.ID+"["+strings.Join(p.Deps(ph.ID), " ")+"]")
	}
	return strings.Join(out, " ")
}

func TestParseAfter(t *testing.T) {
	for _, c := range []struct {
		name, section string
		want          after
		err           string
	}{
		{"ids with commas", "## P3 — X\n\nAfter: P0, P2\n\nGoal.", after{found: true, ids: []string{"P0", "P2"}}, ""},
		{"bold label, spaces, CRLF", "## P3 — X\r\n\r\n**After:** P0 P2\r\n", after{found: true, ids: []string{"P0", "P2"}}, ""},
		{"all in bold, and, a full stop", "## P3 — X\n**After: P0 and P2.**", after{found: true, ids: []string{"P0", "P2"}}, ""},
		{"code spans", "## P3 — X\nAfter: `P0`, `P2`", after{found: true, ids: []string{"P0", "P2"}}, ""},
		{"none", "## P3 — X\nafter: none", after{found: true}, ""},
		{"all", "## P3 — X\nAfter: ALL", after{found: true, all: true}, ""},
		{"not the first line", "## P3 — X\n\nGoal: do it.\nAfter: P0", after{}, ""},
		{"no line", "## P3 — X", after{}, ""},
		{"the heading itself", "## P3 — After: P0\nGoal.", after{}, ""},
		{"another word", "## P3 — X\nAfterwards: P0", after{}, ""},
		{"empty", "## P3 — X\nAfter:", after{found: true}, "names no phase"},
		{"none with ids", "## P3 — X\nAfter: P0, none", after{found: true}, "stands alone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAfter(c.section)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

// A phase's Type: line sits beside its After: line, in either order; `Type: qa` marks a QA phase.
func TestParseHead(t *testing.T) {
	for _, c := range []struct {
		name, section string
		after         after
		typ, err      string
	}{
		{"after, then type", "## P5 — QA\n\nAfter: P2, P4\nType: qa\n\nGoal.", after{found: true, ids: []string{"P2", "P4"}}, TypeQA, ""},
		{"type, then after", "## P5 — QA\nType: QA\nAfter: all", after{found: true, all: true}, TypeQA, ""},
		{"bold, and a phrase", "## P5 — QA\n**Type:** QA phase.\n**After:** P4", after{found: true, ids: []string{"P4"}}, TypeQA, ""},
		{"no type", "## P3 — X\nAfter: P0\nType of work: refactor", after{found: true, ids: []string{"P0"}}, "", ""},
		{"type alone", "## P3 — X\nType: qa\n\nGoal.", after{}, TypeQA, ""},
		{"too far down", "## P3 — X\nGoal.\nType: qa", after{}, "", ""},
		{"unknown type", "## P3 — X\nType: release", after{}, "", `Type: "release" is not a kind of phase baton knows`},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, typ, err := parseHead(c.section)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(a, c.after) || typ != c.typ {
				t.Fatalf("got %+v %q, %v; want %+v %q", a, typ, err, c.after, c.typ)
			}
		})
	}

	doc := "# Demo\n\n## P0 — A\n\nAfter: none\n\n## P1 — B\n\n## P2 — Check\n\nAfter: all\nType: qa\n\n## P3 — C\n\n## Verification\n"
	p, err := Build("demo.md", []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Phases[2].QA() || p.Phases[0].QA() {
		t.Fatalf("phases %+v", p.Phases)
	}
	if q := p.NextQA("P0"); q == nil || q.ID != "P2" {
		t.Errorf("next QA of P0: %+v", q)
	}
	if q := p.NextQA("P2"); q != nil {
		t.Errorf("next QA of the QA phase itself: %+v", q)
	}
	if got := p.SinceQA("P2"); !reflect.DeepEqual(got, []string{"P0", "P1"}) {
		t.Errorf("since QA: %v", got)
	}
	if spec := Suggest([]byte(doc)); spec.Phases[2].Type != TypeQA {
		t.Errorf("the suggested spec lacks the type: %+v", spec.Phases[2])
	}
	spec := Suggest([]byte(doc))
	spec.Phases[1].Type = TypeQA
	if _, err := Build("demo.md", []byte(doc), spec); err == nil || !strings.Contains(err.Error(), "a `Type: qa` line under its heading") {
		t.Errorf("a spec that disagrees with the text: %v", err)
	}
}

func TestBuildDerivesTheGraphFromTheText(t *testing.T) {
	doc := fixture(t, "after.md")
	spec := Suggest(doc)
	p, err := Build("after.md", doc, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := graphOf(p), "P0[] P1[] P2[P1] P3[P0 P2] P4[P0 P1 P2 P3]"; got != want {
		t.Errorf("graph = %s, want %s", got, want)
	}
	// The suggestion carries the same graph, so it round-trips through --spec.
	if got := graphOf(Plan{Phases: spec.Phases}); got != graphOf(p) {
		t.Errorf("suggested graph = %s", got)
	}
	if got, want := p.Dependencies(), "dependencies: P0 and P1 need no other phase; P3 after P0 and P2; P4 after every phase before it; "+
		"every other phase follows the one before it; longest chain 4 of 5 phases"; got != want {
		t.Errorf("Dependencies() = %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(p.Dependents("P2"), []string{"P3", "P4"}) || p.Dependents("P4") != nil {
		t.Errorf("Dependents: %v %v", p.Dependents("P2"), p.Dependents("P4"))
	}
}

func TestBuildRejectsDependenciesItCannotFollow(t *testing.T) {
	doc := string(fixture(t, "after.md"))
	edit := func(old, new string) []byte { return []byte(strings.Replace(doc, old, new, 1)) }
	for _, c := range []struct {
		name string
		doc  []byte
		edit func(*Spec)
		want string
	}{
		{"unknown id", edit("After: P0, P2", "After: P0, P9"), nil, `phase 4 ("P3"): After: names "P9", which is not a phase of this plan`},
		{"itself", edit("After: P0, P2", "After: P3"), nil, "names the phase itself"},
		{"a later phase", edit("**After:** none", "**After:** P3"), nil, "names P3, which comes after it"},
		{"a bad line", edit("After: P0, P2", "After:"), nil, "names no phase"},
		{"a spec that disagrees", []byte(doc), func(s *Spec) { s.Phases[2].After = []string{"P0"} }, `the spec's "after" (P0) differs from the plan's (P1)`},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := Suggest(c.doc)
			spec.Phases = append([]Phase(nil), spec.Phases...)
			if c.edit != nil {
				c.edit(&spec)
			}
			if _, err := Build("after.md", c.doc, spec); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// A spec may repeat the plan's graph, in any order, or leave it out.
	spec := Suggest([]byte(doc))
	spec.Phases[3].After = []string{"P2", "P0"}
	spec.Phases[4].After = nil
	if _, err := Build("after.md", []byte(doc), spec); err != nil {
		t.Errorf("a spec that agrees: %v", err)
	}
}

// Phases stay in document order through Recheck, however they finish, and the dependencies of the phases
// still to run (and of added ones) come from the new text. A done phase keeps what it had.
func TestRecheckKeepsDocumentOrderAndRederivesTheGraph(t *testing.T) {
	base := "# T\n\n## P0 — A\nAfter: none\na\n\n## P1 — B\nAfter: none\nb\n\n## P2 — C\nc\n\n## P3 — D\nAfter: P1\nd\n\n## Verification\nv\n"
	for _, c := range []struct {
		name, old, new string
		remaining      []string
		graph, err     string
	}{
		{"finished out of order", "a\n", "a, edited\n", []string{"P0", "P2", "P3"}, "P0[] P1[] P2[P1] P3[P1]", ""},
		{"a phase added between", "## P2 — C", "## P1b — B2\nAfter: P0\nq\n\n## P2 — C", []string{"P0", "P2", "P3"}, "P0[] P1[] P1b[P0] P2[P1b] P3[P1]", ""},
		{"a dependency edited", "After: P1\nd", "After: P0, P2\nd", []string{"P2", "P3"}, "P0[] P1[] P2[P1] P3[P0 P2]", ""},
		{"all", "After: P1\nd", "After: all\nd", []string{"P3"}, "P0[] P1[] P2[P1] P3[P0 P1 P2]", ""},
		{"a line dropped", "After: P1\nd", "d", []string{"P3"}, "P0[] P1[] P2[P1] P3[P2]", ""},
		{"a done phase's line may break", "After: none\nb", "After: P9\nb", []string{"P2", "P3"}, "P0[] P1[] P2[P1] P3[P1]", ""},
		{"a phase still to run may not", "After: P1\nd", "After: P9\nd", []string{"P3"}, "", `"P9", which is not a phase`},
		{"nor wait for a later one", "## P2 — C\nc", "## P2 — C\nAfter: P3\nc", []string{"P2", "P3"}, "", "comes after it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "plan.md")
			os.WriteFile(file, []byte(base), 0o644)
			p, err := Build(file, []byte(base), Suggest([]byte(base)))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(base, c.old) {
				t.Fatalf("%q is not in the plan", c.old)
			}
			os.WriteFile(file, []byte(strings.Replace(base, c.old, c.new, 1)), 0o644)
			adopted, changed, err := Recheck(p, c.remaining)
			if c.err != "" {
				if !changed || err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("changed %v, err = %v; want %q", changed, err, c.err)
				}
				return
			}
			if !changed || err != nil || adopted.SHA256 != adopted.SHA() {
				t.Fatalf("changed %v, err %v", changed, err)
			}
			if got := graphOf(adopted); got != c.graph {
				t.Errorf("graph = %s, want %s", got, c.graph)
			}
		})
	}
}

// A done phase whose heading was renamed away stays just after the phase it followed.
func TestRecheckPlacesARenamedDonePhase(t *testing.T) {
	base := "# T\n\n## P0 — A\na\n\n## P1 — B\nb\n\n## P2 — C\nc\n"
	file := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(file, []byte(base), 0o644)
	p, _ := Build(file, []byte(base), Suggest([]byte(base)))
	os.WriteFile(file, []byte(strings.Replace(base, "## P1 — B", "## P1 — B (done)", 1)+"\n## P3 — D\nAfter: P1\nd\n"), 0o644)
	adopted, _, err := Recheck(p, []string{"P2"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := graphOf(adopted), "P0[] P1[P0] P2[P1] P3[P1]"; got != want {
		t.Errorf("graph = %s, want %s", got, want)
	}
}

// A phase's section ends at the nearest anchor after it, even when the phase list is not in document
// order (as Recheck once left it, with done phases first).
func TestSectionEndsAtTheNearestAnchor(t *testing.T) {
	doc := []byte("# T\n\n## Standing rules\nr\n\n## P0 — A\na\n\n## P1 — B\nb\n\n## P2 — C\nc\n\n## Verification\nv\n")
	p := Plan{RulesAnchor: "## Standing rules", EndAnchor: "## Verification", Phases: []Phase{
		{ID: "P1", Anchor: "## P1 — B"}, {ID: "P0", Anchor: "## P0 — A"}, {ID: "P2", Anchor: "## P2 — C"},
	}}
	for i, want := range []string{"## P1 — B\nb", "## P0 — A\na", "## P2 — C\nc"} {
		if got, err := p.Section(doc, i); err != nil || got != want {
			t.Errorf("section %d = %q, %v; want %q", i, got, err, want)
		}
	}
	if got := p.Rules(doc); got != "## Standing rules\nr" {
		t.Errorf("rules = %q", got)
	}
}

// A plan.json from before dependencies has no "after": its phases run in order.
func TestOldPlanJSONReadsAsSequential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(path, []byte(`{"version":1,"title":"Old","plan_file":"/p.md","plan_sha256":"x","phases":[`+
		`{"id":"P0","title":"a","anchor":"## P0 — a"},{"id":"P1","title":"b","anchor":"## P1 — b"},{"id":"P2","title":"c","anchor":"## P2 — c"}]}`), 0o644)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[1].After != nil || graphOf(p) != "P0[] P1[P0] P2[P1]" || !p.Sequential() {
		t.Errorf("old plan: %s", graphOf(p))
	}
	if got := p.Dependencies(); got != "dependencies: each phase follows the one before it" {
		t.Errorf("Dependencies() = %q", got)
	}
	// A plan built now always stores "after", even when it waits for none.
	doc := fixture(t, "after.md")
	built, _ := Build("after.md", doc, Suggest(doc))
	b, _ := json.Marshal(built.Phases[0])
	if !strings.Contains(string(b), `"after":[]`) {
		t.Errorf("phase JSON = %s", b)
	}
}

// graph is a plan whose phase Pi waits for deps[i] (nil: the phase before it).
func graph(deps ...[]string) Plan {
	var p Plan
	for i, d := range deps {
		p.Phases = append(p.Phases, Phase{ID: "P" + string(rune('0'+i)), After: d})
	}
	return p
}

func TestAncestors(t *testing.T) {
	none := []string{}
	for _, c := range []struct {
		name string
		p    Plan
		id   string
		want string
	}{
		{"a chain", graph(nil, nil, nil, nil), "P3", "P0 P1 P2"},
		{"a diamond", graph(none, []string{"P0"}, []string{"P0"}, []string{"P1", "P2"}), "P3", "P0 P1 P2"},
		{"one branch of two", graph(none, none, []string{"P1"}, []string{"P0"}, []string{"P2"}), "P4", "P1 P2"},
		{"no dependencies", graph(none, none), "P1", ""},
		{"an unknown phase", graph(none), "P9", ""},
	} {
		if got := strings.Join(c.p.Ancestors(c.id), " "); got != c.want {
			t.Errorf("%s: Ancestors(%s) = %q, want %q", c.name, c.id, got, c.want)
		}
	}
}

func TestDescendants(t *testing.T) {
	none := []string{}
	for _, c := range []struct {
		name string
		p    Plan
		id   string
		want string
	}{
		{"a chain", graph(nil, nil, nil, nil), "P1", "P2 P3"},
		{"a diamond", graph(none, []string{"P0"}, []string{"P0"}, []string{"P1", "P2"}), "P1", "P3"},
		{"one branch of two", graph(none, none, []string{"P1"}, []string{"P0"}, []string{"P2"}), "P1", "P2 P4"},
		{"the last phase", graph(nil, nil), "P1", ""},
		{"an unknown phase", graph(none), "P9", ""},
	} {
		if got := strings.Join(c.p.Descendants(c.id), " "); got != c.want {
			t.Errorf("%s: Descendants(%s) = %q, want %q", c.name, c.id, got, c.want)
		}
	}
}

func TestLongestChain(t *testing.T) {
	none := []string{}
	for _, c := range []struct {
		name  string
		p     Plan
		k     int
		chain string
		deps  string
	}{
		{"chain", graph(nil, nil, nil, nil), 4, "P0 P1 P2 P3", "dependencies: each phase follows the one before it"},
		{"diamond", graph(none, []string{"P0"}, []string{"P0"}, []string{"P1", "P2"}), 3, "P0 P1 P3",
			"dependencies: P2 after P0; P3 after P1 and P2; every other phase follows the one before it; longest chain 3 of 4 phases"},
		{"wide", graph(none, none, none, none), 1, "P0", "dependencies: P0, P1, P2 and P3 need no other phase; longest chain 1 of 4 phases"},
		{"wide, then all", graph(none, none, none, []string{"P0", "P1", "P2"}), 2, "P0 P3",
			"dependencies: P0, P1 and P2 need no other phase; P3 after every phase before it; longest chain 2 of 4 phases"},
		{"two chains", graph(none, none, []string{"P1"}, []string{"P0"}, []string{"P2"}), 3, "P1 P2 P4",
			"dependencies: P0 and P1 need no other phase; P3 after P0; P4 after P2; every other phase follows the one before it; longest chain 3 of 5 phases"},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, chain := c.p.LongestChain()
			if k != c.k || strings.Join(chain, " ") != c.chain {
				t.Errorf("LongestChain = %d %v, want %d %s", k, chain, c.k, c.chain)
			}
			if got := c.p.Dependencies(); got != c.deps {
				t.Errorf("Dependencies() = %q\nwant %q", got, c.deps)
			}
		})
	}
}

// A step is a level of the graph: a phase is one step after the latest of the phases it waits for.
func TestSteps(t *testing.T) {
	none := []string{}
	for _, c := range []struct {
		name string
		p    Plan
		want string
	}{
		{"no phases", Plan{}, ""},
		{"a chain", graph(nil, nil, nil), "P0 | P1 | P2"},
		{"four apart", graph(none, none, none, none), "P0 P1 P2 P3"},
		{"a diamond", graph(none, []string{"P0"}, []string{"P0"}, []string{"P1", "P2"}), "P0 | P1 P2 | P3"},
		{"a chain beside one", graph(none, []string{"P0"}, []string{"P1"}, none), "P0 P3 | P1 | P2"},
		{"after the latest it waits for", graph(none, []string{"P0"}, []string{"P0", "P1"}, []string{"P0"}), "P0 | P1 P3 | P2"},
		{"after every phase before it", graph(none, none, []string{"P0", "P1"}), "P0 P1 | P2"},
	} {
		var steps []string
		for _, s := range c.p.Steps() {
			steps = append(steps, strings.Join(s, " "))
		}
		if got := strings.Join(steps, " | "); got != c.want {
			t.Errorf("%s: Steps() = %q, want %q", c.name, got, c.want)
		}
	}
}

// Width is the most phases that can be worked on at once: phases none of which waits for another,
// directly or through others.
func TestWidth(t *testing.T) {
	none := []string{}
	for _, c := range []struct {
		name string
		p    Plan
		want int
	}{
		{"no phases", Plan{}, 0},
		{"a chain", graph(nil, nil, nil, nil), 1},
		{"four apart", graph(none, none, none, none), 4},
		{"a diamond", graph(none, []string{"P0"}, []string{"P0"}, []string{"P1", "P2"}), 2},
		{"two, then one after both", graph(none, none, []string{"P0", "P1"}), 2},
		{"a chain beside one", graph(none, []string{"P0"}, []string{"P1"}, none), 2},
		{"two chains", graph(none, none, []string{"P1"}, []string{"P0"}, []string{"P2"}), 2},
		{"through others", graph(none, []string{"P0"}, none, []string{"P1"}, []string{"P2"}, []string{"P2"}), 3},
		{"through one phase", graph(none, none, []string{"P0", "P1"}, []string{"P2"}, []string{"P2"}), 2},
	} {
		if got := c.p.Width(); got != c.want {
			t.Errorf("%s: Width() = %d, want %d", c.name, got, c.want)
		}
	}
}
