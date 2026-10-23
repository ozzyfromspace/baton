package plan

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A QA phase's agent works from the plan narrowed to it, which still names the phases it checks, so its
// brief says what it checks as the lead's would; a phase that builds keeps none.
func TestANarrowedQAPhaseKeepsWhatItChecks(t *testing.T) {
	doc := "# Demo\n\n## P0 — A\n\nAfter: none\n\n## P1 — Check\n\nType: qa\n\n## P2 — B\n\n## P2b — C\n\n## P3 — Release check\n\nType: qa\n\n## Verification\n"
	p, err := Build("demo.md", []byte(doc), Suggest([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   string
		want []string
	}{
		{"P0", nil},
		{"P1", []string{"P0"}},
		{"P2", nil},
		{"P3", []string{"P2", "P2b"}},
	} {
		only, err := p.Only(c.id)
		if err != nil {
			t.Fatal(err)
		}
		var back Plan
		if b, err := json.Marshal(only); err != nil || json.Unmarshal(b, &back) != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if got := back.SinceQA(c.id); !reflect.DeepEqual(got, c.want) && (len(got) > 0 || len(c.want) > 0) {
			t.Errorf("%s, narrowed: since QA %q, want %q", c.id, got, c.want)
		}
		if got := p.SinceQA(c.id); c.want != nil && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: since QA %q in the run's plan, want %q", c.id, got, c.want)
		}
	}
}
