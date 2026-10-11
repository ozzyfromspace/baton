package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// switchedAway puts the run of testdata/after.md where a switch leaves it: P0 set aside while its job j1
// ran, and P1 under way. The job has now ended with status.
func (s *session) switchedAway(status string) {
	s.t.Helper()
	pl, err := s.store().LoadPlan()
	if err != nil {
		s.t.Fatal(err)
	}
	origin := state.OriginOf(s.store().Root)
	s.set(func(x *state.State) {
		x.Jobs, x.JobSeq = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, Notes: "the survey is written", Queued: s.now}}, 1
		if _, ok := state.Switch(x, pl, s.now); !ok {
			s.t.Fatal("no switch")
		}
		state.Start(x, s.now, origin)
		x.Run.Compaction.Status = state.CompactDone // the switch's compaction happened
		j := x.Job("j1")
		j.Status, j.Exit, j.Started, j.Ended = status, 1, s.now.Add(-12*time.Minute), s.now
	})
}

// A checkpoint goes back to a phase whose job ended meanwhile: the phase in hand is set aside with the
// checkpoint's notes.
func TestACheckpointGoesBackToAPhaseWithItsResult(t *testing.T) {
	s := afterSession(t)
	s.switchedAway(state.JobFailed)
	out := s.must("", "checkpoint", "--notes", "the users table is in; the orders table is next")
	for _, want := range []string{"baton: checkpoint recorded. P0 can go on now (its job j1 failed (exit 1) after 12m), so baton sets P1 aside here and takes P0 up again; P1 resumes from your notes later.",
		"End your turn now: baton compacts the context, then P0 resumes with a fresh brief."} {
		if !strings.Contains(out, want) {
			t.Errorf("checkpoint says:\n%s\nwant %q", out, want)
		}
	}
	st, _ := s.store().Load()
	ps := st.Phases["P1"]
	if ps.Status != state.PhaseParked || ps.Park.Reason != state.ParkCheckpoint || ps.Park.Notes != "the users table is in; the orders table is next" || ps.Park.Job != "" {
		t.Errorf("P1 %+v park %+v", ps, ps.Park)
	}
	if c := st.Run.Compaction; st.Current != "P0" || !st.BoundaryOwed || st.CheckpointOwed || c.Status != state.CompactQueued || c.Reason != "switch" {
		t.Errorf("in hand %q owed %v checkpoint %v compaction %+v", st.Current, st.BoundaryOwed, st.CheckpointOwed, c)
	}
	if len(st.Run.Announce) != 1 || st.Run.Announce[0] != "baton: checkpoint → P1 set aside; compacting, then P0 resumes (its job j1 failed (exit 1) after 12m)" {
		t.Errorf("announced %q", st.Run.Announce)
	}
	for kind, fields := range map[string]map[string]any{
		"checkpoint":     {"phase": "P1"},
		"phase_parked":   {"phase": "P1", "reason": "checkpoint"},
		"phase_switched": {"from": "P1", "to": "P0", "by": "checkpoint"},
		"compact_queued": {"reason": "switch", "next": "P0", "by": "checkpoint"},
	} {
		if !s.logged(kind, fields) {
			t.Errorf("no %s %v:\n%s", kind, fields, s.events())
		}
	}
}

// A checkpoint stays a checkpoint with nothing to go back to, without notes to resume from, or with the
// phase's work uncommitted, which it says.
func TestACheckpointThatStaysACheckpoint(t *testing.T) {
	for _, c := range []struct {
		name  string
		job   string
		args  []string
		dirty bool
		says  string
	}{
		{"the job still runs", state.JobRunning, []string{"--notes", "half way"}, false, ""},
		{"no notes", state.JobPassed, nil, false, ""},
		{"work uncommitted", state.JobPassed, []string{"--notes", "half way"}, true,
			"baton: note — P0 can go on now (its job j1 passed after 12m), and a checkpoint is where baton takes it up again, but only with P1's work committed."},
	} {
		s := afterSession(t)
		s.switchedAway(c.job)
		if c.dirty {
			gittest.Write(t, s.store().Root, "schema.sql", "create table users;\n")
		}
		out := s.must("", append([]string{"checkpoint"}, c.args...)...)
		if !strings.Contains(out, "baton: checkpoint recorded. End your turn now: baton compacts the context and you continue P1 from your notes.") || !strings.Contains(out, c.says) {
			t.Errorf("%s: checkpoint says:\n%s", c.name, out)
		}
		if st, _ := s.store().Load(); st.Current != "P1" || !st.CheckpointOwed || st.Phases["P1"].Status != state.PhaseActive || st.Run.Compaction.InFlight() {
			t.Errorf("%s: switched: in hand %q, P1 %+v", c.name, st.Current, st.Phases["P1"])
		}
	}
}
