package state

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A red gate stands until the same command passes: a narrower check that passes does not clear it, nor
// does one that checked nothing, while the same command again, or one made the gate in its place
// (--replace), takes over from it as soon as it is queued. A wait on something outside stands on its
// latest, as before: a CI run is waited on again by another command.
func TestARedGateStandsUntilTheSameCommandPasses(t *testing.T) {
	red := Job{ID: "j1", Phase: "P0", Command: "make test", Status: JobFailed, Exit: 1}
	for _, c := range []struct {
		name     string
		jobs     []Job
		standing []string
		redGate  string // RedGate's, "" for none
		red      []string
	}{
		{"red", []Job{red}, []string{"j1"}, "j1", []string{"j1"}},
		{"a narrower gate passed", []Job{red, {ID: "j2", Phase: "P0", Command: "go test -run X ./x", Status: JobPassed}}, []string{"j1"}, "j1", []string{"j1"}},
		{"a narrower gate runs", []Job{red, {ID: "j2", Phase: "P0", Command: "go test -run X ./x", Status: JobRunning}}, []string{"j1"}, "j1", []string{"j1"}},
		{"the same command runs", []Job{red, {ID: "j2", Phase: "P0", Command: "make test", Status: JobRunning}}, []string{"j2"}, "j1", nil},
		{"the same command passed", []Job{red, {ID: "j2", Phase: "P0", Command: "make test", Status: JobPassed}}, []string{"j2"}, "", nil},
		{"the same command failed again", []Job{red, {ID: "j2", Phase: "P0", Command: "make test", Status: JobFailed}}, []string{"j2"}, "j2", []string{"j2"}},
		{"the same command was interrupted", []Job{red, {ID: "j2", Phase: "P0", Command: "make test", Status: JobInterrupted}}, []string{"j1"}, "j1", []string{"j1"}},
		{"made the gate", []Job{red, {ID: "j2", Phase: "P0", Command: "make lint", Replaces: "j1", Status: JobPassed}}, []string{"j2"}, "", nil},
		{"made the gate of another red", []Job{red, {ID: "j2", Phase: "P0", Command: "make lint", Replaces: "j9", Status: JobPassed}}, []string{"j1"}, "j1", []string{"j1"}},
		{"a passing gate, then another command", []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobPassed},
			{ID: "j2", Phase: "P0", Command: "make lint", Status: JobPassed}}, []string{"j2"}, "", nil},
		{"another phase's gate", []Job{red, {ID: "j2", Phase: "P1", Command: "make lint", Status: JobPassed}}, []string{"j1"}, "j1", []string{"j1"}},
		{"beside a wait outside, waited on again", []Job{red, {ID: "j2", Phase: "P0", Kind: JobAwait, Command: "gh run watch 1", Status: JobFailed},
			{ID: "j3", Phase: "P0", Kind: JobAwait, Command: "gh run watch 2", Status: JobPassed}}, []string{"j1", "j3"}, "j1", []string{"j1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{Jobs: c.jobs}
			if got := ids(st.Standing("P0")); !slices.Equal(got, c.standing) {
				t.Errorf("Standing %v, want %v", got, c.standing)
			}
			got := ""
			if g := st.RedGate("P0"); g != nil {
				got = g.ID
			}
			if got != c.redGate {
				t.Errorf("RedGate %q, want %q", got, c.redGate)
			}
			var reds []string
			for _, j := range st.Red() {
				if j.Phase == "P0" {
					reds = append(reds, j.ID)
				}
			}
			if !slices.Equal(reds, c.red) {
				t.Errorf("Red %v, want %v", reds, c.red)
			}
		})
	}
}

// Past MaxJobs, the oldest finished jobs go, but never one whose phase would stand on another red job
// without it: the red gate it stands on, or an old red the same command cleared that would stand again
// once that is gone. A red job that goes keeps its log (KeptLogs).
func TestTheRecordNeverForgetsARedGate(t *testing.T) {
	filler := func(st *State, from int) {
		for i := from; len(st.Jobs) < MaxJobs; i++ {
			st.Jobs = append(st.Jobs, Job{ID: fmt.Sprintf("j%d", i), Phase: "P1", Command: "make test", Status: JobPassed})
		}
	}
	for _, c := range []struct {
		name    string
		jobs    []Job
		dropped string
		kept    []string
	}{
		{"the red gate stands", []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobFailed}}, "j2", nil},
		{"a red the same command cleared", []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobFailed},
			{ID: "j2", Phase: "P0", Command: "make test", Status: JobPassed}}, "j1", []string{"j1"}},
		{"an old red that would stand again", []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobFailed},
			{ID: "j2", Phase: "P0", Command: "make lint", Status: JobFailed}, {ID: "j3", Phase: "P0", Command: "make test", Status: JobPassed}}, "j2", []string{"j2"}},
		{"a passing gate its phase stands on", []Job{{ID: "j1", Phase: "P0", Command: "make test", Status: JobPassed}}, "j1", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{Jobs: slices.Clone(c.jobs)}
			filler(&st, len(c.jobs)+1)
			before := ids(st.Standing("P0"))
			_, _, dropped := QueueJob(&st, Job{Phase: "P2", Command: "make test"}, t0)
			if len(dropped) != 1 || dropped[0].ID != c.dropped {
				t.Fatalf("dropped %v, want %s", ids(dropped), c.dropped)
			}
			if !slices.Equal(st.KeptLogs, c.kept) {
				t.Errorf("kept logs %v, want %v", st.KeptLogs, c.kept)
			}
			if after := ids(st.Standing("P0")); c.dropped != "j1" && !slices.Equal(before, after) {
				t.Errorf("P0 stood on %v, now on %v", before, after)
			}
		})
	}
	// With nothing else to drop, the record grows past MaxJobs rather than lose a red gate.
	st := State{}
	for i := 1; i <= MaxJobs; i++ {
		st.Jobs = append(st.Jobs, Job{ID: fmt.Sprintf("j%d", i), Phase: fmt.Sprintf("P%d", i), Command: "make test", Status: JobFailed})
	}
	if _, _, dropped := QueueJob(&st, Job{Phase: "P0", Command: "make test"}, t0); len(dropped) != 0 || len(st.Jobs) != MaxJobs+1 {
		t.Errorf("dropped %v, %d on record", ids(dropped), len(st.Jobs))
	}
}

// The record of a plan replaced or dropped takes the logs of its failed jobs, on record or dropped from it,
// into its jobs/ folder, and leaves the others'.
func TestARecordTakesTheFailedJobsLogs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), DirName), "i", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(threePhases()); err != nil {
		t.Fatal(err)
	}
	st := underway(t)
	st.Jobs = []Job{
		{ID: "j2", Phase: "P0", Command: "make test", Status: JobPassed},
		{ID: "j3", Phase: "P0", Command: "make test", Status: JobFailed, Exit: 1},
		{ID: "j4", Phase: "P0", Command: "make e2e", Status: JobTimeout},
		{ID: "j5", Phase: "P0", Command: "make test", Status: JobInterrupted},
	}
	st.KeptLogs = []string{"j1", "j0"} // j0's log was pruned before baton kept them
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		os.MkdirAll(filepath.Dir(s.JobLog(id)), 0o755)
		os.WriteFile(s.JobLog(id), []byte(id+" output\n"), 0o644)
	}
	rec, err := s.Record(st)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := rec.Save()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	entries, _ := os.ReadDir(filepath.Join(dir, "jobs"))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if want := []string{"j1.log", "j3.log", "j4.log"}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "jobs", "j3.log")); string(b) != "j3 output\n" {
		t.Errorf("j3's log: %q", b)
	}
	if _, err := os.Stat(s.JobLog("j3")); err != nil {
		t.Errorf("the run's own copy went: %v", err) // a revision goes on with its jobs
	}
}
