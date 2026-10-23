package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A QA phase whose done owed one full run once its rerun passed: the host queues that run as the rerun
// passes, the phase verifying on it, tells the human and logs it; the phase is done once it passes.
func TestTheHostQueuesAQAPhasesConfirmation(t *testing.T) {
	r := resultRig(t)
	r.verifying("P0")
	r.set(func(st *state.State) {
		st.Jobs[0].Reruns = "j0"
		st.JobSeq = 1
		st.Phases["P0"].On = []string{"j1"}
		st.Phases["P0"].Confirm = &state.Confirm{Of: "j0", Command: "make test", Commit: "c3", Files: []string{"src/a.go"}, After: "j1", Notes: "confirm"}
	})
	r.finish("j1", state.JobPassed)
	r.tick(time.Second)
	st := r.state()
	ps, j := st.Phases["P0"], st.Job("j2")
	if ps.Status != state.PhaseVerifying || !slices.Equal(ps.On, []string{"j2"}) || j == nil || !j.Requeue || !j.QAPhase || j.Command != "make test" || j.Commit != "c3" {
		t.Fatalf("P0 %+v, j2 %+v", ps, j)
	}
	const line = "baton: one full run confirms P0's reruns (job j2, `make test`), since files beyond the failing tests changed since the full run (j0): src/a.go"
	if !slices.Contains(st.Run.Announce, line) || strings.Contains(strings.Join(st.Run.Announce, "\n"), "P0 done") {
		t.Errorf("announced %q", st.Run.Announce)
	}
	if ev := r.events(); !strings.Contains(ev, `"kind":"qa_confirm_queued"`) || !strings.Contains(ev, `"by":"settle"`) || strings.Contains(ev, `"kind":"phase_done"`) {
		t.Errorf("events:\n%s", ev)
	}
	r.finish("j2", state.JobPassed)
	r.tick(time.Second)
	if st := r.state(); st.Phases["P0"].Status != state.PhaseDone || st.Job("j3") != nil {
		t.Errorf("P0 %+v, j3 %+v", st.Phases["P0"], st.Job("j3"))
	}
}

// A phase agent's loop queues its QA phase's confirmation as the rerun passes, and tells the human in the
// lead's session: nobody watches the agent's.
func TestAnAgentsLoopQueuesItsQAPhasesConfirmation(t *testing.T) {
	x := newFleet(t)
	pl, err := x.work.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	x.set(x.work, func(st *state.State) {
		state.Start(st, x.now, state.Origin{})
		q, _, _ := state.QueueJob(st, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test", Commit: "c2", Reruns: "j0"}, x.now)
		id = q.ID
		j := st.Job(id)
		j.Status, j.Started, j.PID = state.JobRunning, x.now, 4242
		if _, err := state.Done(st, pl, "P1", x.now, state.DoneOpts{}); err != nil {
			t.Fatal(err)
		}
		st.Phases["P1"].Confirm = &state.Confirm{Of: "j0", Command: "make test", Commit: "c3", Files: []string{"src/a.go"}, After: id}
	})
	x.set(x.work, func(st *state.State) {
		j := st.Job(id)
		j.Status, j.Ended = state.JobPassed, x.now
	})
	x.tick(time.Second)
	st, _ := x.work.Load()
	if ps := st.Phases["P1"]; ps.Status != state.PhaseVerifying || st.Mode == state.ModeComplete || ps.Confirm.Job == "" || len(ps.On) != 1 || ps.On[0] != ps.Confirm.Job {
		t.Fatalf("P1 %+v, confirm %+v; the sub-run %s", ps, ps.Confirm, st.Mode)
	}
	if !hasEvent(x.events(x.work), "qa_confirm_queued", map[string]any{"phase": "P1", "by": "settle"}) {
		t.Errorf("the sub-run's events:\n%s", x.events(x.work))
	}
	lead, _ := x.lead.Load()
	if !slices.ContainsFunc(lead.Run.Announce, func(l string) bool { return strings.HasPrefix(l, "baton: one full run confirms P1's reruns (job ") }) {
		t.Errorf("told the lead %q", lead.Run.Announce)
	}
}
