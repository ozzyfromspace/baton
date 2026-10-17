package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A brief lists the run's facts, each with the phase that found it, and how to record another; none, nothing.
func TestEnvFacts(t *testing.T) {
	if got := EnvFacts(nil); got != "" {
		t.Errorf("no facts: %q", got)
	}
	got := EnvFacts([]state.Fact{{Text: "leave host port 5432 free", Phase: "P2"}, {Text: "start colima first"}})
	for _, w := range []string{"What this run's gates need of their environment, as its phases found it (baton env), oldest first:\n- leave host port 5432 free (P2)\n- start colima first (the lead)\n",
		"a lane has only what .baton-lane sets up", "record it with `baton env \"<what the environment needs>\"`"} {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q:\n%s", w, got)
		}
	}
}

// What the agents at work, the human and the model that recorded a fact are told.
func TestFactWords(t *testing.T) {
	f := state.Fact{Text: "leave host port 5432 free", Phase: "P2"}
	for _, c := range []struct{ got, want string }{
		{FactTold(f), "[baton] P2 found what this run's gates need of their environment, and recorded it (baton env): leave host port 5432 free. Allow for it in your gates: a lane has only what .baton-lane sets up."},
		{FactRecorded(f, nil), "baton: P2 recorded what the gates' environment needs — leave host port 5432 free → every later brief lists it"},
		{FactRecorded(f, []string{"P3", "P4"}), "baton: P2 recorded what the gates' environment needs — leave host port 5432 free → every later brief lists it; P3 and P4 told"},
		{FactSaid(f, 2, nil), "baton: recorded, as the run's environment fact 2: leave host port 5432 free. Every later brief in this run lists it. Make it permanent in .baton-lane too, so that every lane is set up with it."},
		{FactSaid(f, 1, []string{"P3"}), "baton: recorded, as the run's environment fact 1: leave host port 5432 free. Every later brief in this run lists it, and P3, at work now, is told. Make it permanent"},
		{FactKnown(state.Fact{Text: "start colima first"}), "baton: already on record, from the lead: start colima first. Nothing new to record."},
		{FactTooLong(412), "at most 300 characters (this one is 412)"},
		{FactsFull(), "this run holds 30 environment facts already"},
	} {
		if !strings.HasPrefix(c.got, c.want) && !strings.Contains(c.got, c.want) {
			t.Errorf("got %q\nwant %q", c.got, c.want)
		}
	}
}
