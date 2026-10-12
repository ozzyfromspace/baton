package decide

import "testing"

func TestCannotGoOn(t *testing.T) {
	const why = "the last turn failed with an API error baton cannot retry away (invalid_request)"
	for _, c := range []struct {
		name string
		stop Stop
		want string
	}{
		{"no workers", Stop{Phase: "P3", Dependents: []string{"P4"}},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; nothing else runs until then."},
		{"workers, nothing waits", Stop{Phase: "P3", Workers: true},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with the rest of the plan."},
		{"workers, one phase waits", Stop{Phase: "P3", Workers: true, Dependents: []string{"P9"}},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with every phase that does not need P3, and P9 waits for it."},
		{"workers, phases wait", Stop{Phase: "P3", Workers: true, Dependents: []string{"P9", "P10", "P20"}},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with every phase that does not need P3. P9, P10 and P20 wait for it."},
		{"no phase in hand", Stop{Workers: true},
			"the lead session stopped: " + why + ". Only you can decide how it goes on; meanwhile workers carry on with the rest of the plan."},
		// A worker stopped leaves the lead running: the rest of the run always goes on.
		{"a worker, nothing waits", Stop{Phase: "P3", Worker: true},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on, in the worker's session (baton view P3); meanwhile the rest of the run carries on."},
		{"a worker, phases wait", Stop{Phase: "P3", Worker: true, Dependents: []string{"P9", "P10"}},
			"P3 stopped: " + why + ". Only you can decide how P3 goes on, in the worker's session (baton view P3); meanwhile the rest of the run carries on " +
				"with every phase that does not need P3. P9 and P10 wait for it."},
	} {
		if got := CannotGoOn(c.stop, why); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestRefused(t *testing.T) {
	const ways = "To go on, rewind (Esc Esc) to before the flagged turn and take a different approach, as Claude Code advises, or /clear for a fresh conversation"
	for _, c := range []struct {
		name     string
		stop     Stop
		category string
		want     string
	}{
		{"the lead, no workers", Stop{Phase: "P3", Dependents: []string{"P4"}}, "cyber",
			"P3 stopped: the model's safeguards declined the last turn (cyber), and this conversation will not go on as it is. baton does not retry it. " +
				ways + " from P3's brief. Nothing else runs until then."},
		{"the lead, workers, phases wait", Stop{Phase: "P3", Workers: true, Dependents: []string{"P9", "P10"}}, "cyber",
			"P3 stopped: the model's safeguards declined the last turn (cyber), and this conversation will not go on as it is. baton does not retry it. " +
				ways + " from P3's brief. Meanwhile workers carry on with every phase that does not need P3. P9 and P10 wait for it."},
		{"no category", Stop{Phase: "P3", Workers: true}, "",
			"P3 stopped: the model's safeguards declined the last turn, and this conversation will not go on as it is. baton does not retry it. " +
				ways + " from P3's brief. Meanwhile workers carry on with the rest of the plan."},
		{"no phase in hand", Stop{}, "cyber",
			"the lead session stopped: the model's safeguards declined the last turn (cyber), and this conversation will not go on as it is. baton does not retry it. " +
				ways + ". Nothing else runs until then."},
		{"a worker", Stop{Phase: "P3", Worker: true, Dependents: []string{"P9"}}, "cyber",
			"P3 stopped: the model's safeguards declined the last turn of its worker (cyber), and that conversation will not go on as it is. baton does not retry it. " +
				"To go on, in the worker's session (baton view P3), rewind (Esc Esc) to before the flagged turn and take a different approach, as Claude Code advises, " +
				"or /clear for a fresh conversation from P3's brief. Meanwhile the rest of the run carries on with every phase that does not need P3, and P9 waits for it."},
	} {
		if got := Refused(c.stop, c.category); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
