package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// GateCost is one line of what phase has spent in gates so far (state.GateCost), and in the command its cost
// was read for (Cost.Of): "P53 so far: 11 gate runs, 1h05m of gates in its 1h45m; `make test-degraded` 7
// of them, 50m." It is empty for a phase with fewer than 2 gate runs, whose result already says what its
// one run took.
func GateCost(phase string, c state.Cost) string {
	if c.Runs < 2 {
		return ""
	}
	s := fmt.Sprintf("%s so far: %d gate runs, %s of gates", phase, c.Runs, Span(c.Gates))
	if c.Phase > 0 {
		s += " in its " + Span(c.Phase)
	}
	switch {
	case c.Of == "" || c.Command == 0:
	case c.Command == c.Runs:
		s += fmt.Sprintf("; all of them `%s`", c.Of)
	default:
		s += fmt.Sprintf("; `%s` %d of them, %s", c.Of, c.Command, Span(c.CommandTime))
	}
	return s + "."
}
