package cli

import (
	"slices"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate built from no stages is rerun narrowed (v0.8.11): `baton verify --failed -- <command>` runs the
// model's command in place of the gate's when it names every test the gate's log says failed
// (lane.Unnamed), and it stands in for the gate as a staged --failed does (Job.Reruns, Job.Narrows): a
// pass clears it. The failures are read from the logs of the runs it draws on (state.Rerun.From), before
// the run's lock.

// askNarrowed works out a narrowed rerun for phase of r, the gate --failed reruns, which recorded no stages:
// command, the one given, must name every test that failed in it. It returns a refusal when no command can
// stand in for the gate, or command does not.
func askNarrowed(s *state.Store, a rerunAsk, r state.Rerun, phase, command string, bring []string) (rerunAsk, error) {
	if len(bring) > 0 {
		return a, refusal{"unknown stages", decide.NarrowNoStages(r.Of, bring)}
	}
	if r.Of.Status == state.JobTimeout {
		return a, refusal{"timed out", decide.NarrowTimedOut(r.Of)}
	}
	fs, more := failuresIn(s, r.From)
	switch {
	case more > 0:
		return a, refusal{"too many failures", decide.NarrowTooMany(r.Of)}
	case len(fs) == 0:
		return a, refusal{"no stages", decide.NoStages(phase, r.Of)}
	case command == "":
		return a, refusal{"no command", decide.NarrowCommandless(r.Of, fs)}
	case command == r.Of.Command:
		return a, refusal{"the whole gate", decide.NarrowWhole(r.Of, fs)}
	}
	if left := lane.Unnamed(command, fs); len(left) > 0 {
		return a, refusal{"a failure unnamed", decide.NarrowUnnamed(r.Of, command, left, fs)}
	}
	a.r, a.narrow, a.given = r, fs, command
	return a, nil
}

// failuresIn are the tests the logs of jobs say failed (lane.FailureIDs), each once, in the order first
// read: at most lane.MaxFailures, and how many more. A log that cannot be read adds none.
func failuresIn(s *state.Store, jobs []state.Job) ([]lane.FailedTest, int) {
	var out []lane.FailedTest
	more := 0
	for _, j := range jobs {
		log := j.Log
		if log == "" {
			log = s.JobLog(j.ID)
		}
		fs, n, err := lane.FailureIDs(log, 0)
		if err != nil {
			continue
		}
		more += n
		for _, f := range fs {
			if !slices.ContainsFunc(out, f.Same) {
				out = append(out, f)
			}
		}
	}
	if len(out) > lane.MaxFailures {
		more += len(out) - lane.MaxFailures
		out = out[:lane.MaxFailures]
	}
	return out, more
}

// command is what the rerun runs: the command given, narrowed; the gate's own, staged.
func (a rerunAsk) command() string {
	if a.narrow != nil {
		return a.given
	}
	return a.r.Of.Command
}

// names are the failures a narrowed rerun names (Job.Narrows), each once; none for a staged rerun.
func (a rerunAsk) names() []string {
	var ids []string
	for _, f := range a.narrow {
		if !slices.Contains(ids, f.ID) {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// standsIn reports whether rerun a stands in for job g, so that a check of another command does not clash
// with g, nor leave it standing red: g is the gate it reruns, or a run of it the rerun draws on
// (state.Rerun.From). False for no rerun.
func (a *rerunAsk) standsIn(g state.Job) bool {
	return a != nil && (g.ID == a.r.Of.ID || slices.ContainsFunc(a.r.From, func(j state.Job) bool { return j.ID == g.ID }))
}
