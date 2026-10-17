package cli

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
)

// A phase agent's done records the land check it gives, for the merge queue to verify its landing again
// with; the lead's own phase, which no merge queue lands, records none and says so.
func TestDoneRecordsALandCheck(t *testing.T) {
	const check = "go test -run TestOne ./one"
	for _, c := range []struct {
		name  string
		agent bool
		want  string
		check string // what the phase records
	}{
		{"a phase agent's", true, decide.LandCheckRecorded("P1", check), check},
		{"the lead's own", false, decide.LandCheckLead("P0"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead := asWorker(t)
			s, phase := lead, "P0"
			if c.agent {
				s, phase = w, "P1"
			}
			out := s.must("", "done", phase, "--notes", "Its gate is the whole suite.", "--land-check", check)
			if !strings.Contains(out, c.want) {
				t.Errorf("done says:\n%s\nwant %q", out, c.want)
			}
			if got := s.state().Phases[phase].LandCheck; got != c.check {
				t.Errorf("%s's land check is %q, want %q", phase, got, c.check)
			}
			if logged := s.logged("phase_done", map[string]any{"phase": phase, "land_check": check}); logged != c.agent {
				t.Errorf("phase_done logs the land check: %v\n%s", logged, s.events())
			}
		})
	}
}
