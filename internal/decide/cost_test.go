package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

func TestGateCostLine(t *testing.T) {
	cases := []struct {
		c    state.Cost
		want string
	}{
		{state.Cost{Runs: 11, Gates: 65 * time.Minute, Phase: 105 * time.Minute, Of: "make test-degraded", Command: 7, CommandTime: 50 * time.Minute},
			"P53 so far: 11 gate runs, 1h05m of gates in its 1h45m; `make test-degraded` 7 of them, 50m."},
		{state.Cost{Runs: 3, Gates: 40 * time.Minute, Phase: 90 * time.Minute, Of: "make test", Command: 3, CommandTime: 40 * time.Minute},
			"P53 so far: 3 gate runs, 40m of gates in its 1h30m; all of them `make test`."},
		{state.Cost{Runs: 2, Gates: 10 * time.Minute, Phase: time.Hour, Of: "go vet ./..."},
			"P53 so far: 2 gate runs, 10m of gates in its 1h00m."},
		{state.Cost{Runs: 2, Gates: 90 * time.Second}, "P53 so far: 2 gate runs, 1m of gates."},
		{state.Cost{Runs: 1, Gates: 12 * time.Minute, Phase: time.Hour, Of: "make test", Command: 1, CommandTime: 12 * time.Minute}, ""},
		{state.Cost{}, ""},
	}
	for _, c := range cases {
		if got := GateCost("P53", c.c); got != c.want {
			t.Errorf("%+v:\n%s\nwant\n%s", c.c, got, c.want)
		}
	}
}
