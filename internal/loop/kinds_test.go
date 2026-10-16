package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The phase in hand waits on its gate and a wait on something outside: one known to run long switches the
// session, the phase is set aside on both, and the switch is called off only once neither is left to wait
// for, or one has not passed.
func TestASwitchOnAGateAndAWait(t *testing.T) {
	r := switchRig(t)
	r.set(func(st *state.State) {
		ci := state.Job{ID: "j0", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch 6 --exit-status", Status: state.JobPassed,
			Started: r.now.Add(-time.Hour), Ended: r.now.Add(-30 * time.Minute), Delivered: r.now}
		st.Jobs = append([]state.Job{ci}, st.Jobs...)
		st.Jobs = append(st.Jobs, state.Job{ID: "j2", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch 6 --exit-status", Status: state.JobRunning,
			Queued: r.now, Notes: "pushed; CI checks it"})
	})
	r.view.LastHumanKey = r.now // the human types as the switch is queued
	r.tick(time.Second)
	st := r.state()
	ps := st.Phases["P0"]
	if !r.switched() || ps.Park.Job != "j2" || ps.Park.Notes != "pushed; CI checks it" || !slices.Equal(ps.On, []string{"j1", "j2"}) {
		t.Fatalf("no switch at once on the wait known to run long: P0 %+v park %+v\n%s", ps, ps.Park, r.events())
	}
	if want := "baton: P0 waits on jobs j1 (`make test`) and j2 (`gh run watch 6 --exit-status`) → set aside; compacting, then P1 starts, and P0 resumes once they have passed, or one has not"; !slices.Contains(st.Run.Announce, want) {
		t.Errorf("announced %q", st.Run.Announce)
	}
	r.finish("j1", state.JobPassed)
	r.tick(time.Second)
	if !r.switched() {
		t.Fatalf("called off with the wait still running: %+v", r.state())
	}
	r.finish("j2", state.JobFailed)
	r.tick(time.Second)
	if st := r.state(); st.Current != "P0" || st.Phases["P0"].Status != state.PhaseActive || st.Run.Compaction.Status != state.CompactCancelled {
		t.Fatalf("not called off on the red wait: in hand %q, %+v", st.Current, st.Run.Compaction)
	}
	if !strings.Contains(r.events(), `"kind":"switch_cancelled"`) {
		t.Errorf("events:\n%s", r.events())
	}
}

// A phase verified on its gate and its wait names both; one reopened names the job that decided.
func TestAVerdictOnAGateAndAWait(t *testing.T) {
	gate := state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobPassed}
	wait := state.Job{ID: "j2", Phase: "P0", Kind: state.JobAwait, Status: state.JobPassed}
	red := wait
	red.Status, red.Exit = state.JobFailed, 1
	for _, c := range []struct {
		vd   state.Verdict
		want string
	}{
		{state.Verdict{Phase: "P0", Job: wait, Jobs: []state.Job{gate, wait}}, "baton: P0 verified (jobs j1 and j2 passed) → P0 done"},
		{state.Verdict{Phase: "P0", Job: gate, Jobs: []state.Job{gate}}, "baton: P0 verified (job j1 passed) → P0 done"},
		{state.Verdict{Phase: "P0", Job: red, Jobs: []state.Job{gate, red}, Park: state.ParkRed}, "baton: P0's verification failed (exit 1) after 0s (job j2) → P0 reopens"},
	} {
		if got := verdictLine(c.vd); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
