package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// `baton stop` ends a job of the caller's own, the lead's or a phase agent's, queued or running: at a gate's
// first failure, say, so that `baton verify --failed` reruns what failed at once instead of waiting for the
// rest of the gate. The job ends as interrupted, so it checked nothing and its phase's red gate stands as
// it was, and its runner ends its process group at its next beat, finding it no longer wanted
// (state.StopJob).

func init() {
	register("stop", "stop a job of yours, queued or running, as at a gate's first failure: stop <job> [--why \"<why>\"]. "+
		"It ends as interrupted, so it checked nothing, and a red gate stands as it was; verify --failed then reruns what failed", cmdStop)
}

func cmdStop(args []string, io IO) int {
	p, err := parseArgs(args, []string{"why"}, nil)
	if err != nil || len(p.pos) != 1 || strings.TrimSpace(p.pos[0]) == "" {
		return fail(io, "usage: baton stop <job> [--why \"<why you stop it>\"]%s", errSuffix(err))
	}
	id, why := strings.TrimSpace(p.pos[0]), decide.Sanitize(p.vals["why"])
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	before, err := s.Load()
	if err != nil {
		return fail(io, "%v", err)
	}
	// What --failed reruns once the job has stopped needs the gates' stages, read before the run's lock.
	stages := map[string][]lane.Stage{}
	for _, j := range before.Jobs {
		if j.Kind != state.JobAwait {
			stages[j.ID] = s.StagesOf(j)
		}
	}
	of := func(j state.Job) []lane.Stage {
		if x, ok := stages[j.ID]; ok {
			return x
		}
		return j.Stages
	}
	var stopped state.Job
	var red *state.Job
	var rerun, waits bool
	_, err = s.Update(func(st *state.State) error {
		j := st.Job(id)
		if j == nil && st.IDPrefix != "" {
			j = st.Job(st.IDPrefix + id) // a phase agent's job, named without its phase
		}
		switch {
		case j == nil:
			return refusal{"no such job", decide.StopUnknown(id, st.Unfinished())}
		case j.Finished():
			return refusal{"ended", decide.StopEnded(*j)}
		}
		ps := st.Phases[j.Phase]
		waits = ps != nil && ps.Status == state.PhaseVerifying &&
			slices.ContainsFunc(st.WaitsOn(j.Phase), func(o state.Job) bool { return o.ID == j.ID })
		stopped, _ = state.StopJob(st, j.ID, decide.StopReason(why), io.Now())
		if g := st.RedGate(stopped.Phase); g != nil {
			x := *g
			red = &x
		}
		_, miss := st.RerunOf(stopped.Phase, of)
		rerun = stopped.Kind != state.JobAwait && miss == ""
		st.Run.Announce = append(st.Run.Announce, decide.StoppedLine(stopped, why))
		return nil
	})
	if err != nil {
		return refused(io, s, "stop", err)
	}
	ev := map[string]any{"job": stopped.ID, "phase": stopped.Phase, "type": stopped.Kind, "command": stopped.Command, "was": stopped.Status}
	if why != "" {
		ev["why"] = why
	}
	if stopped.Here {
		ev["here"] = true
	}
	if red != nil {
		ev["red_gate"] = red.ID // stands as it was
	}
	s.Event("job_stopped", ev)
	fmt.Fprintln(io.Out, "baton: "+decide.Stopped(stopped, laneTick, red, rerun, waits))
	return 0
}
