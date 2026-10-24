package loop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

const killedOOM = "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"

// killedGate records job id of s as a whole run of `bin/qa-set` for phase that ended red at ended, its build
// stage killed for memory beside a red test stage: its log, and its stages file as `baton stage` writes it.
func killedGate(t *testing.T, s *state.Store, phase, id string, ended time.Time) {
	t.Helper()
	log := "::baton stage unit start\n--- FAIL: TestLogin (0.20s)\n::baton stage unit failed exit=1 seconds=95\n" +
		"::baton stage check-bundle-budgets start\nnuxt build\n" + killedOOM + "\n::baton stage check-bundle-budgets failed exit=134 seconds=612\n"
	path := s.JobLog(id)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, st := range []lane.Stage{
		{Name: "unit", Status: lane.StageFailed, Exit: 1, Failures: []string{"TestLogin"}, Runner: "go"},
		{Name: "check-bundle-budgets", Status: lane.StageFailed, Exit: 134, Unnarrowed: lane.UnnarrowedNoIDs},
	} {
		if err := lane.AppendStage(s.JobStages(id), st); err != nil {
			t.Fatal(err)
		}
	}
	s.Update(func(st *state.State) error {
		st.Jobs = append(st.Jobs, state.Job{ID: id, Phase: phase, Kind: state.JobVerify, Command: "bin/qa-set", Commit: "abc", Status: state.JobFailed, Exit: 1,
			Started: ended.Add(-41 * time.Minute), Ended: ended})
		return nil
	})
}

// A second whole run of the lead's phase's gate that loses its build to memory, as the first did, pushes the
// human once, with a line in the session and an event; the first run, and a third, push nothing.
func TestASecondKilledRunIsPushedOnce(t *testing.T) {
	r := resultRig(t)
	s := r.loop.Store
	killedGate(t, s, "P1", "j1", r.now)
	r.tick(time.Second)
	r.tick(time.Second)
	if len(r.push.kinds) != 0 || r.state().Job("j1").EnvRead.IsZero() {
		t.Fatalf("pushed %v after one run; j1 %+v", r.push.kinds, r.state().Job("j1"))
	}

	killedGate(t, s, "P1", "j2", r.now)
	r.tick(time.Second)
	r.tick(time.Second)
	const text = "P1's gate lost `check-bundle-budgets` to memory in two whole runs (j1, j2): the machine, Docker's VM say, may be too small for it. " +
		"The lead works around it meanwhile."
	if !slices.Equal(r.push.kinds, []string{"env_repeated"}) || r.push.details[0] != text {
		t.Fatalf("pushed %v %q", r.push.kinds, r.push.details)
	}
	st := r.state()
	if !slices.Contains(st.Run.Announce, "baton: "+text) || !slices.Equal(st.Phases["P1"].EnvPushed, []string{lane.EnvKilled}) {
		t.Errorf("announced %q, pushed %v", st.Run.Announce, st.Phases["P1"].EnvPushed)
	}
	if lost := st.Job("j2").EnvLost; len(lost) != 1 || lost[0] != (state.EnvLoss{Stage: "check-bundle-budgets", Kind: lane.EnvKilled, Line: killedOOM}) {
		t.Errorf("j2 lost %+v", lost)
	}
	if !hasEvent(r.events(), "env_repeated", map[string]any{"phase": "P1", "env_kind": lane.EnvKilled, "command": "bin/qa-set", "jobs": "[j1 j2]", "stages": "[check-bundle-budgets]"}) {
		t.Errorf("events:\n%s", r.events())
	}

	killedGate(t, s, "P1", "j3", r.now)
	r.run(time.Minute)
	if len(r.push.kinds) != 1 || strings.Count(r.events(), `"kind":"env_repeated"`) != 1 {
		t.Errorf("pushed again: %v\n%s", r.push.kinds, r.events())
	}
}

// A phase agent's loop pushes the same, from its sub-run, and tells the human in the lead's session: nobody
// watches the agent's.
func TestAnAgentsSecondKilledRunIsPushedOnce(t *testing.T) {
	x := newFleet(t)
	p := &pushes{}
	x.agent.loop.Notify = p
	x.set(x.work, func(st *state.State) { state.Start(st, x.now, state.Origin{}) })
	killedGate(t, x.work, "P1", "P1.j1", x.now)
	killedGate(t, x.work, "P1", "P1.j2", x.now.Add(time.Minute))
	x.tick(time.Second)
	x.tick(time.Second)
	const text = "P1's gate lost `check-bundle-budgets` to memory in two whole runs (P1.j1, P1.j2): the machine, Docker's VM say, may be too small for it. " +
		"The agent works around it meanwhile."
	if !slices.Equal(p.kinds, []string{"env_repeated"}) || p.details[0] != text || p.projects[0] != "demo · P1" {
		t.Fatalf("pushed %v %q to %q", p.kinds, p.details, p.projects)
	}
	lead, _ := x.lead.Load()
	if !slices.Contains(lead.Run.Announce, "baton: "+text) {
		t.Errorf("told the lead %q", lead.Run.Announce)
	}
	if work, _ := x.work.Load(); slices.Contains(work.Run.Announce, "baton: "+text) {
		t.Errorf("told the agent's own session %q", work.Run.Announce)
	}
	if !hasEvent(x.events(x.work), "env_repeated", map[string]any{"phase": "P1", "jobs": "[P1.j1 P1.j2]"}) {
		t.Errorf("the sub-run's events:\n%s", x.events(x.work))
	}
}
