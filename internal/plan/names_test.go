package plan

import (
	"strings"
	"testing"
)

// Every phase id names its phase's agent in Claude Code: letters, digits, '_' and '-' (no dot), at most
// 32 characters, none of the names Claude Code keeps for itself in any case, nothing of the shape of an
// agent id, and no two equal ignoring case. Build refuses any other with why.
func TestPhaseIDsAreAgentNames(t *testing.T) {
	doc := fixture(t, "headings-emdash.md")
	for _, c := range []struct {
		name string
		ids  [3]string // the plan's three phases
		want string    // what the error says, "" for a plan that builds
	}{
		{"the skill's ids", [3]string{"P0", "P1", "R1"}, ""},
		{"a split", [3]string{"P0", "P8b", "P9"}, ""},
		{"another notation", [3]string{"Phase-A", "Phase-B", "phase_c"}, ""},
		{"32 characters", [3]string{"P0", "P1", "P" + strings.Repeat("x", 31)}, ""},
		{"a reserved word inside", [3]string{"mainline", "users", "system-2"}, ""},
		{"hex, not an agent id's shape", [3]string{"P0", "abcdef0123456789", "a0123456789abcde"}, ""},

		{"a dot", [3]string{"P0", "P1.5", "P2"}, `phase 2 ("P1.5"): id must be 1–32 letters, digits, '_' or '-' (no '.')`},
		{"a space", [3]string{"P 0", "P1", "P2"}, `phase 1 ("P 0"): id must be`},
		{"a leading dash", [3]string{"P0", "-P1", "P2"}, `phase 2 ("-P1"): id must be`},
		{"33 characters", [3]string{"P0", "P1", "P" + strings.Repeat("x", 32)}, "phase 3 (\"Pxx"},
		{"main", [3]string{"main", "P1", "P2"}, `phase 1 ("main"): id is a name Claude Code keeps for itself`},
		{"Main, in another case", [3]string{"P0", "Main", "P2"}, `phase 2 ("Main"): id is a name Claude Code keeps`},
		{"team-lead", [3]string{"P0", "P1", "TEAM-LEAD"}, `phase 3 ("TEAM-LEAD"): id is a name Claude Code keeps`},
		{"user", [3]string{"user", "P1", "P2"}, `phase 1 ("user"): id is a name Claude Code keeps`},
		{"system", [3]string{"P0", "System", "P2"}, `phase 2 ("System"): id is a name Claude Code keeps`},
		{"an agent id", [3]string{"P0", "a0123456789abcdef", "P2"}, `phase 2 ("a0123456789abcdef"): id has the shape of a Claude Code agent id`},
		{"an agent id in capitals", [3]string{"A0123456789ABCDEF", "P1", "P2"}, `phase 1 ("A0123456789ABCDEF"): id has the shape`},
		{"an agent id with a prefix", [3]string{"P0", "P1", "aworker-0123456789abcdef"}, `phase 3 ("aworker-0123456789abcdef"): id has the shape`},
		{"a duplicate", [3]string{"P0", "P0", "P2"}, `phase 2 ("P0"): duplicate id`},
		{"a duplicate ignoring case", [3]string{"P0", "P1", "p1"}, `phase 3 ("p1"): id equals P1's ignoring case`},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := Suggest(doc)
			for i := range spec.Phases {
				spec.Phases[i].ID, spec.Phases[i].After = c.ids[i], nil
			}
			_, err := Build("plan.md", doc, spec)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// A replan's draft builds by the same rule: a phase it adds with an id equal to another's ignoring case
// does not build. An id the plan had already passes as it is (one attached before baton checked names),
// in a replan's draft and in a plan file baton follows.
func TestARedraftsIDsAreAgentNames(t *testing.T) {
	doc := string(fixture(t, "phase-letters.md"))
	old, err := Build("letters.md", []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	draft := edited(t, doc, "## Phase C\n", "## Phase b — Again\nMove the rest.\n\n## Phase C\n")
	_, bs := Redraft(old, []byte(doc), draft, map[string]bool{"Phase-A": true}, Limits{Add: true, Edit: true, Drop: true})
	if len(bs) != 1 || bs[0].Kind != BreachBuild || !strings.Contains(bs[0].Detail, `("Phase-b"): id equals Phase-B's ignoring case`) {
		t.Errorf("breaches: %+v", bs)
	}
	if _, _, err := Follow(old, draft, []string{"Phase-B", "Phase-C"}); err == nil || !strings.Contains(err.Error(), "id equals Phase-B's ignoring case") {
		t.Errorf("followed: %v", err)
	}

	// A plan of bold bullets keeps the ids it was given, and one of them has a dot.
	bullets := string(fixture(t, "bold-bullets.md"))
	legacy, err := Build("bullets.md", []byte(bullets), Suggest([]byte(bullets)))
	if err != nil {
		t.Fatal(err)
	}
	legacy.Phases[1].ID = "P1.5"
	next := edited(t, bullets, "router and packages.\n", "router and packages.\n  Then a Makefile.\n")
	p, bs := Redraft(legacy, []byte(bullets), next, map[string]bool{"P0": true}, Limits{Edit: true})
	if len(bs) != 0 || p.Index("P1.5") != 1 {
		t.Errorf("a legacy id in a redraft: %+v, %v", bs, ids(Spec{Phases: p.Phases}))
	}
	if _, changed, err := Follow(legacy, next, []string{"P1.5", "P2"}); err != nil || !changed {
		t.Errorf("a legacy id followed: changed %v, %v", changed, err)
	}
}

// Named finds the phase a name addresses, ignoring case, as Claude Code matches an agent's name.
func TestNamed(t *testing.T) {
	doc := fixture(t, "phase-letters.md")
	p, err := Build("letters.md", doc, Suggest(doc))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Phase-A": "Phase-A", "phase-b": "Phase-B", " PHASE-C ": "Phase-C", "Phase": "", "": "", "P0": ""} {
		if got := p.Named(name); got != want {
			t.Errorf("Named(%q) = %q, want %q", name, got, want)
		}
	}
}
