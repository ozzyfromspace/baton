package plan

import (
	"reflect"
	"strings"
	"testing"
)

// A phase's Model: and Effort: lines sit beside its After:, Type: and Replan: lines, in any order, and say
// its tier. A full model id or a display name reads as its family.
func TestParseHeadTier(t *testing.T) {
	for _, c := range []struct {
		name, section string
		after         after
		typ           string
		model, effort string
		err           string
	}{
		{"both", "## P1 — Build\n\nModel: opus\nEffort: xhigh\n\nBuild it.", after{}, "", "opus", "xhigh", ""},
		{"with every other line, After: last", "## P5 — QA\nType: qa\nReplan: after\nModel: sonnet\nEffort: high\nAfter: P2, P4\n\nGoal.",
			after{found: true, ids: []string{"P2", "P4"}}, TypeQA, "sonnet", "high", ""},
		{"bold, any case, a full stop", "## P1 — Build\n**Model:** Opus.\n**Effort:** MAX", after{}, "", "opus", "max", ""},
		{"a full id", "## P1 — Build\nModel: claude-opus-5-5", after{}, "", "opus", "", ""},
		{"a display name", "## P1 — Build\nModel: Sonnet 5.5", after{}, "", "sonnet", "", ""},
		{"Claude Code's spellings", "## P1 — Build\nEffort: med", after{}, "", "", "medium", ""},
		{"ultracode is xhigh", "## P1 — Build\nEffort: ultracode", after{}, "", "", "xhigh", ""},
		{"effort alone", "## P1 — Build\nAfter: none\nEffort: low", after{found: true}, "", "", "low", ""},
		{"too far down", "## P1 — Build\nAfter: none\nGoal.\nModel: opus", after{found: true}, "", "", "", ""},
		{"an unknown model", "## P1 — Build\nModel: gpt-9", after{}, "", "", "", `Model: "gpt-9" is not a model baton can give a phase`},
		{"an unknown effort", "## P1 — Build\nEffort: huge", after{}, "", "", "", `Effort: "huge" is not an effort level`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := parseHead(c.section)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(h.after, c.after) || h.typ != c.typ || h.model != c.model || h.effort != c.effort {
				t.Fatalf("got %+v, %v; want %+v %q %q/%q", h, err, c.after, c.typ, c.model, c.effort)
			}
		})
	}
}

// A plan built from its text gives each phase its tier, and lists the phases that leave a part of it to the
// session; a spec that says another tier than the text is refused.
func TestATieredPlan(t *testing.T) {
	doc := "# Demo\n\n## P0 — Spike\n\nAfter: none\nModel: opus\nEffort: max\n\nTry it.\n\n## P1 — Build\n\nModel: sonnet\nEffort: medium\n\nBuild it.\n\n## P2 — Ship\n\nEffort: low\n\nShip it.\n"
	p, err := Build("demo.md", []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	want := []Tier{{"opus", "max"}, {"sonnet", "medium"}, {"", "low"}}
	for i, ph := range p.Phases {
		if ph.Tier() != want[i] || p.TierOf(ph.ID) != want[i] {
			t.Errorf("%s: %+v, want %+v", ph.ID, ph.Tier(), want[i])
		}
	}
	if !p.Tiered() || !reflect.DeepEqual(p.Untiered(), []string{"P2"}) {
		t.Errorf("tiered %v, untiered %v", p.Tiered(), p.Untiered())
	}
	if d := p.Deps("P1"); len(d) != 1 || d[0] != "P0" {
		t.Errorf("the tier lines hid P1's place: after %v", d)
	}
	spec := Suggest([]byte(doc))
	spec.Phases[1].Model, spec.Phases[1].Effort = "opus", "high"
	if _, err := Build("demo.md", []byte(doc), spec); err == nil || !strings.Contains(err.Error(), "the spec's tier (opus/high) differs from the plan's (sonnet/medium)") {
		t.Errorf("a spec with another tier: %v", err)
	}
	if _, err := Build("demo.md", []byte(strings.Replace(doc, "Effort: low", "Effort: lots", 1)), Suggest([]byte(doc))); err == nil ||
		!strings.Contains(err.Error(), `phase 3 ("P2"): Effort: "lots" is not an effort level`) {
		t.Errorf("an unknown effort: %v", err)
	}
	plain := "# Demo\n\n## P0 — Spike\n\nTry it.\n\n## P1 — Build\n\nBuild it.\n"
	if q, err := Build("demo.md", []byte(plain), Suggest([]byte(plain))); err != nil || q.Tiered() || len(q.Untiered()) != 2 {
		t.Errorf("a plan with no tier lines: %+v, %v", q.Phases, err)
	}
}

// A change of structure leaves a phase's tier lines in its body, so a replan that only adds an After: line
// changes nothing else, and one that changes a tier changes the body.
func TestBodyKeepsTheTier(t *testing.T) {
	sec := "## P1 — Build\n\nModel: opus\nEffort: high\n\nBuild it."
	added := "## P1 — Build\n\nModel: opus\nEffort: high\nAfter: none\n\nBuild it."
	if Body(sec) != Body(added) {
		t.Errorf("an After: line under the tier lines changed the body:\n%q\n%q", Body(sec), Body(added))
	}
	if Body(sec) == Body(strings.Replace(sec, "Effort: high", "Effort: low", 1)) {
		t.Error("a new effort left the body as it was")
	}
}

// Raise climbs effort first, as far as xhigh, then the next allowed family, then max on the highest; a tier
// that leaves a part to the session climbs from the session's.
func TestRaise(t *testing.T) {
	std := []string{"haiku", "sonnet", "opus"}
	all := []string{"haiku", "sonnet", "opus", "fable"}
	for _, c := range []struct {
		from, base Tier
		allowed    []string
		want       Tier
		ok         bool
	}{
		{Tier{"opus", "high"}, Tier{}, std, Tier{"opus", "xhigh"}, true},
		{Tier{"opus", "xhigh"}, Tier{}, std, Tier{"opus", "max"}, true},
		{Tier{"opus", "xhigh"}, Tier{}, all, Tier{"fable", "xhigh"}, true},
		{Tier{"opus", "max"}, Tier{}, std, Tier{"opus", "max"}, false},
		{Tier{"fable", "max"}, Tier{}, all, Tier{"fable", "max"}, false},
		{Tier{"sonnet", "medium"}, Tier{}, std, Tier{"sonnet", "high"}, true},
		{Tier{"sonnet", "xhigh"}, Tier{}, std, Tier{"opus", "xhigh"}, true},
		{Tier{"haiku", "xhigh"}, Tier{}, []string{"haiku", "opus"}, Tier{"opus", "xhigh"}, true},
		{Tier{"", "high"}, Tier{"opus", "medium"}, std, Tier{"opus", "xhigh"}, true},
		{Tier{"opus", ""}, Tier{"opus", "medium"}, std, Tier{"opus", "high"}, true},
		{Tier{"opus", ""}, Tier{}, std, Tier{"opus", ""}, false},
		{Tier{"", ""}, Tier{}, std, Tier{}, false},
	} {
		got, ok := Raise(c.from, c.base, c.allowed)
		if got != c.want || ok != c.ok {
			t.Errorf("Raise(%s, %s, %v) = %s, %v; want %s, %v", c.from, c.base, c.allowed, got, ok, c.want, c.ok)
		}
	}
	climb, steps := Tier{"sonnet", "medium"}, []string{}
	for t2, ok := Raise(climb, Tier{}, std); ok; t2, ok = Raise(t2, Tier{}, std) {
		steps = append(steps, t2.String())
	}
	if got := strings.Join(steps, " "); got != "sonnet/high sonnet/xhigh opus/xhigh opus/max" {
		t.Errorf("sonnet/medium climbs %s", got)
	}
}

// Family reads a model's family from an alias, a full id or a display name.
func TestFamily(t *testing.T) {
	for in, want := range map[string]string{
		"opus": "opus", "Opus": "opus", "claude-opus-5-5": "opus", "claude-sonnet-5-5": "sonnet", "claude-haiku-5-5": "haiku",
		"claude-fable-5-1": "fable", "Fable 5.1": "fable", "opus-5-5": "opus", "claude-mythos-5": "", "": "", "<synthetic>": "",
	} {
		if got := Family(in); got != want {
			t.Errorf("Family(%q) = %q, want %q", in, got, want)
		}
	}
}
