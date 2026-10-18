package state

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A phase's `baton done` is asked about what it left running once: the record says so, reads back as
// written, and a state with none writes no key.
func TestAPhaseIsAskedAboutWhatItLeftOnce(t *testing.T) {
	st := New()
	if st.AskedLeftovers("P3") || !AskLeftovers(&st, "P3") || AskLeftovers(&st, "P3") || !st.AskedLeftovers("P3") || st.AskedLeftovers("P4") {
		t.Fatalf("asked %v", st.Run.LeftoverAsked)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil || !slices.Equal(back.Run.LeftoverAsked, []string{"P3"}) {
		t.Errorf("read back %v, %v", back.Run.LeftoverAsked, err)
	}
	if b, _ := json.Marshal(New()); strings.Contains(string(b), "leftover_asked") {
		t.Errorf("a state never asked writes the key: %s", b)
	}
}
