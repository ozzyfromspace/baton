package state

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A second whole run of a phase's gate that loses a stage to the same kind as an earlier one makes a push
// due, once per phase and kind; a rerun, another command, another phase's run or another kind does not.
func TestASecondLossToTheSameKindIsPushedOnce(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	killed := []EnvLoss{{Stage: "check-bundle-budgets", Kind: lane.EnvKilled, Line: "JavaScript heap out of memory"}}
	port := []EnvLoss{{Stage: "up", Kind: "a port already in use", Line: "port is already allocated"}}
	gate := func(id, phase, command string, ended int, j Job) Job {
		j.ID, j.Phase, j.Kind, j.Command, j.Status, j.Exit = id, phase, JobVerify, command, JobFailed, 1
		j.Ended = t0.Add(time.Duration(ended) * time.Minute)
		return j
	}
	for _, c := range []struct {
		name  string
		jobs  []Job
		lost  map[string][]EnvLoss // what each job's log shows, read in this order
		order []string
		want  []EnvRepeat
	}{
		{name: "two whole runs", jobs: []Job{gate("P15.j1", "P15", "bin/qa-set", 40, Job{}), gate("P15.j4", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"P15.j1": killed, "P15.j4": killed}, order: []string{"P15.j1", "P15.j4"},
			want: []EnvRepeat{{Phase: "P15", Command: "bin/qa-set", Kind: lane.EnvKilled, Stages: []string{"check-bundle-budgets"}, Jobs: []string{"P15.j1", "P15.j4"}}}},
		{name: "once per phase and kind", jobs: []Job{gate("j1", "P15", "bin/qa-set", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{}), gate("j3", "P15", "bin/qa-set", 140, Job{})},
			lost: map[string][]EnvLoss{"j1": killed, "j2": killed, "j3": append(slices.Clone(killed), EnvLoss{Stage: "build", Kind: lane.EnvKilled})}, order: []string{"j1", "j2", "j3"},
			want: []EnvRepeat{{Phase: "P15", Command: "bin/qa-set", Kind: lane.EnvKilled, Stages: []string{"check-bundle-budgets"}, Jobs: []string{"j1", "j2"}}}},
		{name: "two stages, both named", jobs: []Job{gate("j1", "P15", "bin/qa-set", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost:  map[string][]EnvLoss{"j1": killed, "j2": {{Stage: "build", Kind: lane.EnvKilled}, killed[0]}},
			order: []string{"j1", "j2"},
			want:  []EnvRepeat{{Phase: "P15", Command: "bin/qa-set", Kind: lane.EnvKilled, Stages: []string{"check-bundle-budgets", "build"}, Jobs: []string{"j1", "j2"}}}},
		{name: "a rerun between is no whole run", jobs: []Job{gate("j1", "P15", "bin/qa-set", 40, Job{Reruns: "j0"}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"j1": killed, "j2": killed}, order: []string{"j1", "j2"}},
		{name: "another command", jobs: []Job{gate("j1", "P15", "make e2e", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"j1": killed, "j2": killed}, order: []string{"j1", "j2"}},
		{name: "another phase", jobs: []Job{gate("j1", "P14", "bin/qa-set", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"j1": killed, "j2": killed}, order: []string{"j1", "j2"}},
		{name: "another kind", jobs: []Job{gate("j1", "P15", "bin/qa-set", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"j1": port, "j2": killed}, order: []string{"j1", "j2"}},
		{name: "one loss", jobs: []Job{gate("j1", "P15", "bin/qa-set", 40, Job{}), gate("j2", "P15", "bin/qa-set", 90, Job{})},
			lost: map[string][]EnvLoss{"j2": killed}, order: []string{"j1", "j2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{Phases: map[string]*PhaseState{"P14": {Status: PhaseActive}, "P15": {Status: PhaseActive}}, Jobs: c.jobs}
			var got []EnvRepeat
			for _, id := range c.order {
				got = append(got, ReadEnv(&st, id, c.lost[id], t0.Add(3*time.Hour))...)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("due %+v, want %+v", got, c.want)
			}
			for _, id := range c.order {
				if j := st.Job(id); j.EnvRead.IsZero() || !reflect.DeepEqual(j.EnvLost, c.lost[id]) {
					t.Errorf("%s recorded %v, %+v", id, j.EnvRead, j.EnvLost)
				}
			}
			if again := ReadEnv(&st, c.order[len(c.order)-1], c.lost[c.order[len(c.order)-1]], t0.Add(4*time.Hour)); again != nil {
				t.Errorf("read again: %+v", again)
			}
			if len(c.want) > 0 && !slices.Equal(st.Phases["P15"].EnvPushed, []string{lane.EnvKilled}) {
				t.Errorf("pushed %v", st.Phases["P15"].EnvPushed)
			}
			if len(EnvUnread(st)) != 0 {
				t.Errorf("unread %+v", EnvUnread(st))
			}
		})
	}
}

// The host reads the whole runs that ended red or were stopped, once: never a rerun, a narrowed rerun, an
// await, a pass, a run that could not run, one under way, or one it read already.
func TestTheHostReadsEachWholeRunOnce(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	st := State{Jobs: []Job{
		{ID: "j1", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Ended: t0.Add(time.Minute)},
		{ID: "j2", Kind: JobVerify, Command: "bin/qa-set", Status: JobInterrupted, Ended: t0.Add(2 * time.Minute)},
		{ID: "j3", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Reruns: "j2", Ended: t0.Add(3 * time.Minute)},
		{ID: "j4", Kind: JobVerify, Command: "bin/qa-set", Status: JobFailed, Narrows: []string{"TestA"}, Ended: t0.Add(4 * time.Minute)},
		{ID: "j5", Kind: JobAwait, Command: "bin/qa-set", Status: JobFailed, Ended: t0.Add(5 * time.Minute)},
		{ID: "j6", Kind: JobVerify, Command: "bin/qa-set", Status: JobPassed, Ended: t0.Add(6 * time.Minute)},
		{ID: "j7", Kind: JobVerify, Command: "bin/qa-set", Status: JobLaneError, Ended: t0.Add(7 * time.Minute)},
		{ID: "j8", Kind: JobVerify, Command: "bin/qa-set", Status: JobTimeout, EnvRead: t0, Ended: t0.Add(8 * time.Minute)},
		{ID: "j9", Kind: JobVerify, Command: "bin/qa-set", Status: JobRunning},
		{ID: "j10", Command: "make e2e", Status: JobFailed, Ended: t0.Add(10 * time.Minute)},
	}}
	var ids []string
	for _, j := range EnvUnread(st) {
		ids = append(ids, j.ID)
	}
	if !slices.Equal(ids, []string{"j1", "j2", "j10"}) {
		t.Errorf("unread %v", ids)
	}
}
