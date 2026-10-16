package plan

import (
	"reflect"
	"strings"
	"testing"
)

// chainDoc is a plan of six phases in one chain, its last a QA phase, with standing rules and an end
// section: what a replan restructures.
const chainDoc = `# Chains

Why this plan exists.

## Standing rules

- Be careful.

## P0 — Schema

Build the schema.

## P1 — API

Build the API.

## P2 — CLI

Build the CLI.

## P3 — Docs

Write the docs.

## P4 — Web

Build the web app.

## P5 — QA

Type: qa

Run everything.

## Verification

All green.
`

// edited is doc with each pair of olds replaced by its new, once; a test fails on an old doc lacks.
func edited(t *testing.T, doc string, pairs ...string) []byte {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(doc, pairs[i]) {
			t.Fatalf("the plan has no %q", pairs[i])
		}
		doc = strings.Replace(doc, pairs[i], pairs[i+1], 1)
	}
	return []byte(doc)
}

func chainPlan(t *testing.T) Plan {
	t.Helper()
	p, err := Build("chain.md", []byte(chainDoc), Suggest([]byte(chainDoc)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// breachList is breaches as "kind phase" strings, in order.
func breachList(bs []Breach) []string {
	var out []string
	for _, b := range bs {
		out = append(out, strings.TrimSpace(b.Kind+" "+b.Phase))
	}
	return out
}

// A replan's draft is checked against what it may change: the phases that started (P0 done, P1 in hand)
// never change, a phase not started is dropped, added or reworded and the goal sections changed only as its
// limits allow, a QA phase stays one, and the draft builds (no After: naming a later or unknown phase).
func TestRedraft(t *testing.T) {
	human := Limits{Add: true, Edit: true}
	words := Limits{Add: true, Edit: true, Drop: true, Goal: true}
	split := "## P3 — Docs"
	for _, c := range []struct {
		name  string
		draft []byte
		lim   Limits
		want  []string
	}{
		{"the After: lines of phases not started", edited(t, chainDoc, "Write the docs.", "After: P1\n\nWrite the docs.", "Build the web app.", "After: P1\n\nBuild the web app."), human, nil},
		{"the same under a structure-only limit", edited(t, chainDoc, "Write the docs.", "After: P1\n\nWrite the docs.", "Build the web app.", "After: P1\n\nBuild the web app."), Limits{}, nil},
		{"phases not started put in another order", edited(t, chainDoc,
			"## P3 — Docs\n\nWrite the docs.\n\n## P4 — Web\n\nBuild the web app.\n",
			"## P4 — Web\n\nAfter: P1\n\nBuild the web app.\n\n## P3 — Docs\n\nAfter: P2\n\nWrite the docs.\n"), Limits{}, nil},
		{"a frozen phase reworded", edited(t, chainDoc, "Build the API.", "Build the API fast."), words, []string{"text P1"}},
		{"a frozen phase gone", edited(t, chainDoc, "## P0 — Schema\n\nBuild the schema.\n\n", ""), words, []string{"gone P0", "deps P1"}},
		{"a phase put before a frozen one", edited(t, chainDoc, "## P1 — API", "## P0b — Prep\n\nPrep.\n\n## P1 — API"), human, []string{"deps P1"}},
		{"the standing rules changed", edited(t, chainDoc, "- Be careful.", "- Be very careful."), human, []string{"goal rules"}},
		{"the standing rules changed at the human's word", edited(t, chainDoc, "- Be careful.", "- Be very careful."), words, nil},
		{"the text before the first phase changed", edited(t, chainDoc, "Why this plan exists.", "Why it exists."), human, []string{"goal preamble"}},
		{"the end section changed", edited(t, chainDoc, "All green.", "All green, twice."), human, []string{"goal end"}},
		{"a phase not started dropped", edited(t, chainDoc, "## P3 — Docs\n\nWrite the docs.\n\n", ""), human, []string{"dropped P3"}},
		{"a phase not started dropped at the human's word", edited(t, chainDoc, "## P3 — Docs\n\nWrite the docs.\n\n", ""), words, nil},
		{"a split", edited(t, chainDoc, split, "## P2b — CLI docs\n\nAfter: P2\n\nDocument the CLI.\n\n"+split), human, nil},
		{"a split where none may be added", edited(t, chainDoc, split, "## P2b — CLI docs\n\nAfter: P2\n\nDocument the CLI.\n\n"+split), Limits{Edit: true}, []string{"added P2b"}},
		{"a phase not started reworded", edited(t, chainDoc, "Build the CLI.", "Build the CLI and its man page."), human, nil},
		{"a phase not started reworded where none may be", edited(t, chainDoc, "Build the CLI.", "Build the CLI and its man page."), Limits{}, []string{"edited P2"}},
		{"a QA phase that is one no more", edited(t, chainDoc, "Type: qa\n\n", ""), words, []string{"qa P5"}},
		{"an After: naming a later phase", edited(t, chainDoc, "Build the CLI.", "After: P4\n\nBuild the CLI."), words, []string{"build"}},
		{"an After: naming no phase of the plan", edited(t, chainDoc, "Build the CLI.", "After: P9\n\nBuild the CLI."), words, []string{"build"}},
		{"two phases with one id", edited(t, chainDoc, "## P3 — Docs", "## P2 — Docs"), words, []string{"build"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := chainPlan(t)
			p, bs := Redraft(old, []byte(chainDoc), c.draft, map[string]bool{"P0": true, "P1": true}, c.lim)
			if got := breachList(bs); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("breaches %v, want %v (%+v)", got, c.want, bs)
			}
			if len(c.want) == 0 && (p.File != "chain.md" || p.Title != "Chains" || p.RulesAnchor == "" || p.EndAnchor != "## Verification") {
				t.Errorf("plan %+v", p)
			}
		})
	}
}

// A draft that does not build says why, as Build does.
func TestRedraftSaysWhyADraftDoesNotBuild(t *testing.T) {
	_, bs := Redraft(chainPlan(t), []byte(chainDoc), edited(t, chainDoc, "Build the CLI.", "After: P4\n\nBuild the CLI."), nil, Limits{})
	if len(bs) != 1 || !strings.Contains(bs[0].Detail, `After: names P4, which comes after it`) {
		t.Fatalf("breaches %+v", bs)
	}
}

// Compare says what a replan changed: the goal sections, then each phase in the new order (added, waiting
// for other phases, reworded, moved), then the phases dropped. Without the old text, it compares the
// phases and what they wait for alone.
func TestCompare(t *testing.T) {
	old := chainPlan(t)
	draft := edited(t, chainDoc,
		"- Be careful.", "- Be very careful.",
		"## P2 — CLI\n\nBuild the CLI.\n\n## P3 — Docs\n\nWrite the docs.\n\n## P4 — Web\n\nBuild the web app.\n",
		"## P4 — Web\n\nAfter: P1\n\nBuild the web app, mobile first.\n\n## P2 — CLI\n\nAfter: P1\n\nBuild the CLI.\n\n## P2b — CLI docs\n\nAfter: P2\n\nDocument the CLI.\n")
	p, bs := Redraft(old, []byte(chainDoc), draft, map[string]bool{"P0": true, "P1": true}, Limits{Add: true, Edit: true, Drop: true, Goal: true})
	if len(bs) > 0 {
		t.Fatalf("breaches %+v", bs)
	}
	want := []Change{
		{Kind: ChangeGoal, Phase: GoalRules},
		{Kind: ChangeAfter, Phase: "P4", Was: []string{"P3"}, Now: []string{"P1"}},
		{Kind: ChangeText, Phase: "P4"},
		{Kind: ChangeMoved, Phase: "P4"},
		{Kind: ChangeAdded, Phase: "P2b", Now: []string{"P2"}},
		{Kind: ChangeAfter, Phase: "P5", Was: []string{"P4"}, Now: []string{"P2b"}},
		{Kind: ChangeDropped, Phase: "P3"},
	}
	if got := Compare(old, []byte(chainDoc), p, draft); !reflect.DeepEqual(got, want) {
		t.Errorf("Compare:\n got %+v\nwant %+v", got, want)
	}
	idsOnly := []Change{
		{Kind: ChangeAfter, Phase: "P4", Was: []string{"P3"}, Now: []string{"P1"}},
		{Kind: ChangeMoved, Phase: "P4"},
		{Kind: ChangeAdded, Phase: "P2b", Now: []string{"P2"}},
		{Kind: ChangeAfter, Phase: "P5", Was: []string{"P4"}, Now: []string{"P2b"}},
		{Kind: ChangeDropped, Phase: "P3"},
	}
	if got := Compare(old, nil, p, draft); !reflect.DeepEqual(got, idsOnly) {
		t.Errorf("Compare without the old text:\n got %+v\nwant %+v", got, idsOnly)
	}
	if got := Compare(old, []byte(chainDoc), old, []byte(chainDoc)); len(got) != 0 {
		t.Errorf("Compare of a plan with itself: %+v", got)
	}
}

// The shape of the phases not started is the shape of the plan narrowed to them, the phases they wait for
// outside it taken as done.
func TestPartShape(t *testing.T) {
	for _, c := range []struct {
		name  string
		doc   []byte
		chain string
		width int
	}{
		{"a chain", []byte(chainDoc), "P2 P3 P4 P5", 1},
		{"three leaves before the QA phase", edited(t, chainDoc, "Write the docs.", "After: P1\n\nWrite the docs.", "Build the web app.", "After: P1\n\nBuild the web app.",
			"Type: qa", "After: all\nType: qa"), "P2 P5", 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := Build("chain.md", c.doc, Suggest(c.doc))
			if err != nil {
				t.Fatal(err)
			}
			part := p.Part([]string{"P2", "P3", "P4", "P5"})
			s := part.Shape()
			if s.Phases != 4 || strings.Join(s.Chain, " ") != c.chain || s.Width != c.width {
				t.Errorf("shape %+v, want chain %s, width %d", s, c.chain, c.width)
			}
			if d := part.Deps("P2"); len(d) != 0 {
				t.Errorf("P2 waits for %v in the part", d)
			}
		})
	}
}

// A phase's body is its section without its After: line, a run of blank lines read as one: what a change
// of structure leaves as it was.
func TestBody(t *testing.T) {
	plain := "## P1 — API\n\nBuild the API.\n\nType: qa"
	for _, c := range []struct {
		name, section string
		same          bool
	}{
		{"as it was", plain, true},
		{"an After: line added", "## P1 — API\n\nAfter: P0\n\nBuild the API.\n\nType: qa", true},
		{"an After: line right under the heading", "## P1 — API\nAfter: none\n\nBuild the API.\n\nType: qa", true},
		{"a bold After: line", "## P1 — API\n\n**After:** P0, P3\n\nBuild the API.\n\nType: qa", true},
		{"a step changed", "## P1 — API\n\nAfter: P0\n\nBuild the API twice.\n\nType: qa", false},
		{"its Type: line gone", "## P1 — API\n\nBuild the API.", false},
		{"an After: line further down is text", "## P1 — API\n\nBuild the API.\n\nType: qa\n\nAfter: P0", false},
	} {
		if got := Body(c.section) == Body(plain); got != c.same {
			t.Errorf("%s: same body %v, want %v (%q)", c.name, got, c.same, Body(c.section))
		}
	}
}

// A plan whose phases are not headings keeps its anchors where the draft still has them.
func TestRebuildKeepsTheAnchorsOfAPlanOfBullets(t *testing.T) {
	doc := fixture(t, "bold-bullets.md")
	old, err := Build("bullets.md", doc, Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Rebuild(old, doc, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Phases, old.Phases) || p.Title != old.Title || p.EndAnchor != old.EndAnchor {
		t.Errorf("rebuilt %+v\nwas %+v", p, old)
	}
}

// A heading reworded takes its new title; one as it was keeps the title the plan gave it.
func TestRebuildKeepsTitlesOfHeadingsAsTheyWere(t *testing.T) {
	old := chainPlan(t)
	old.Phases[2].Title = "The CLI" // as a corrected spec named it
	p, err := Rebuild(old, []byte(chainDoc), edited(t, chainDoc, "## P3 — Docs", "## P3 — Docs and help"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[2].Title != "The CLI" || p.Phases[3].Title != "Docs and help" {
		t.Errorf("titles %q, %q", p.Phases[2].Title, p.Phases[3].Title)
	}
}
