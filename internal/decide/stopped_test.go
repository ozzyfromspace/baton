package decide

import "testing"

func TestCannotGoOn(t *testing.T) {
	const why = "the last turn failed with an API error baton cannot retry away (invalid_request)"
	for _, c := range []struct {
		name, phase string
		waiting     []string
		workers     bool
		want        string
	}{
		{"no workers", "P3", []string{"P4"}, false,
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; nothing else runs until then."},
		{"workers, nothing waits", "P3", nil, true,
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with the rest of the plan."},
		{"workers, one phase waits", "P3", []string{"P9"}, true,
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with every phase that does not need P3, and P9 waits for it."},
		{"workers, phases wait", "P3", []string{"P9", "P10", "P20"}, true,
			"P3 stopped: " + why + ". Only you can decide how P3 goes on; meanwhile workers carry on with every phase that does not need P3. P9, P10 and P20 wait for it."},
		{"no phase in hand", "", nil, true,
			"the lead session stopped: " + why + ". Only you can decide how it goes on; meanwhile workers carry on with the rest of the plan."},
	} {
		if got := CannotGoOn(c.phase, why, c.waiting, c.workers); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
