package state

import (
	"testing"
	"time"
)

// A phase's gates are counted from when each began to run to its end, or now, a stretch two ran at once
// counted once; awaits, the merge queue's re-verifications and gates that never ran are not its gates.
func TestGateCost(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	gate := func(phase, cmd string, ran, ended int) Job {
		j := Job{Phase: phase, Kind: JobVerify, Command: cmd, Status: JobPassed, Started: at(ran - 1), Ran: at(ran)}
		if ended >= 0 {
			j.Ended = at(ended)
		} else {
			j.Status = JobRunning
		}
		return j
	}
	cases := []struct {
		name    string
		st      State
		command string
		now     int
		want    Cost
	}{
		{
			name: "overlapping gates",
			st: State{Phases: map[string]*PhaseState{"P1": {Status: PhaseActive, StartedAt: at(0)}}, Jobs: []Job{
				gate("P1", "make test", 10, 30),
				gate("P1", "make lint", 20, 40), // 10m of it beside make test
				gate("P1", "make test", 50, 60),
				gate("P2", "make test", 0, 100),                                    // another phase's
				{Phase: "P1", Kind: JobAwait, Command: "gh run watch", Ran: at(0)}, // a wait outside
				{Phase: "P1", Kind: JobVerify, Command: "make test", Requeue: true, Ran: at(70), Ended: at(80)},
				{Phase: "P1", Kind: JobVerify, Command: "make test", Status: JobInterrupted, Queued: at(5), Ended: at(6)}, // never ran
			}},
			command: "make test", now: 90,
			want: Cost{Runs: 3, Gates: 40 * time.Minute, Phase: 90 * time.Minute, Of: "make test", Command: 2, CommandTime: 30 * time.Minute},
		},
		{
			name: "a running gate counts to now, and one from before its start is the phase's",
			st: State{Phases: map[string]*PhaseState{"P1": {Status: PhaseActive, StartedAt: at(30)}}, Jobs: []Job{
				gate("P1", "make e2e", 10, 20),
				gate("P1", "make e2e", 40, -1),
				{Phase: "P1", Command: "make e2e", Started: at(25), Ended: at(28)}, // an old record's: no kind, no Ran
			}},
			command: "make e2e", now: 55,
			want: Cost{Runs: 3, Gates: 28 * time.Minute, Phase: 45 * time.Minute, Of: "make e2e", Command: 3, CommandTime: 28 * time.Minute},
		},
		{
			name: "an agent's phase starts with its agent",
			st: State{Generation: 2,
				Phases: map[string]*PhaseState{"P3": {Status: PhaseWorking, StartedAt: at(0), Worker: "w/P3"}},
				Workers: []Worker{
					{Phase: "P3", Ref: "w/P3", Generation: 1, Started: at(1)},
					{Phase: "P3", Ref: "w/P3", Generation: 2, Assigned: at(0), Started: at(5)},
				},
				Jobs: []Job{gate("P3", "go test ./...", 10, 15), gate("P3", "go test ./...", 20, 25)}},
			command: "go vet ./...", now: 65,
			want: Cost{Runs: 2, Gates: 10 * time.Minute, Phase: time.Hour, Of: "go vet ./..."},
		},
		{
			name:    "one run",
			st:      State{Phases: map[string]*PhaseState{"P1": {StartedAt: at(0)}}, Jobs: []Job{gate("P1", "make test", 1, 3)}},
			command: "make test", now: 4,
			want: Cost{Runs: 1, Gates: 2 * time.Minute, Phase: 4 * time.Minute, Of: "make test", Command: 1, CommandTime: 2 * time.Minute},
		},
		{
			name: "no record of the phase", st: State{}, command: "make test", now: 4,
			want: Cost{Of: "make test"},
		},
	}
	for _, c := range cases {
		if got := GateCost(c.st, phaseOf(c.st), c.command, at(c.now)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// phaseOf is the one phase a case's state has, or P1.
func phaseOf(st State) string {
	for id := range st.Phases {
		return id
	}
	return "P1"
}
