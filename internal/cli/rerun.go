package cli

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// `baton verify --failed` queues the phase's red gate's command again as a rerun (state.Rerun): the stages
// that passed in it are carried, each failed one runs narrowed to its failures, and a pass clears it. What
// it reruns is worked out from the stages records read before the run's lock (askRerun), and again under
// it (still), with the same records; the record it runs from is written beside its log once it is queued
// (state.Store.SaveRerun).

// rerunAsk is what a --failed rerun reruns, as worked out before the run's lock.
type rerunAsk struct {
	r      state.Rerun
	bring  []string                // the stages run whole again (--stages)
	record []lane.Stage            // the record it runs from (state.RerunRecord)
	stages map[string][]lane.Stage // the phase's gates' stages, as read
	saved  error                   // why the record could not be written (save)
}

// askRerun works out what --failed reruns for phase, as st stands: command is the one given with it, if
// any, and bring the stages --stages brings back. It returns a refusal when there is nothing to rerun, or
// what was asked does not fit it.
func askRerun(s *state.Store, st state.State, phase, command string, bring []string) (rerunAsk, error) {
	a := rerunAsk{bring: bring, stages: map[string][]lane.Stage{}}
	for _, j := range st.Jobs {
		if j.Phase == phase && j.Kind != state.JobAwait {
			a.stages[j.ID] = s.StagesOf(j)
		}
	}
	r, why := st.RerunOf(phase, a.of)
	switch why {
	case state.RerunNoGate:
		var running *state.Job
		for _, j := range st.Running(phase) {
			if j.Kind != state.JobAwait {
				x := j
				running = &x
			}
		}
		return a, refusal{"no red gate", decide.NoRedGate(phase, running)}
	case state.RerunNoStages:
		return a, refusal{"no stages", decide.NoStages(phase, r.Of)}
	}
	if command != "" && command != r.Of.Command {
		return a, refusal{"another command", decide.FailedCommand(r.Of, command)}
	}
	if bad := state.Unrecorded(r.Stages, bring); len(bad) > 0 {
		return a, refusal{"unknown stages", decide.UnknownStages(r.Of, bad, r.Stages)}
	}
	a.r, a.record = r, state.RerunRecord(r.Stages, bring)
	return a, nil
}

// of is job j's stages as askRerun read them.
func (a rerunAsk) of(j state.Job) []lane.Stage {
	if stages, ok := a.stages[j.ID]; ok {
		return stages
	}
	return j.Stages
}

// still turns down the rerun when, under the run's lock, phase's record no longer gives the gate askRerun
// found.
func (a rerunAsk) still(st state.State, phase string) error {
	if r, why := st.RerunOf(phase, a.of); why != "" || r.Of.ID != a.r.Of.ID {
		return refusal{"jobs changed", decide.RerunChanged(phase)}
	}
	return nil
}

// stageList reads --stages: names split on commas, blanks left out.
func stageList(v string) []string {
	var out []string
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// save writes the record rerun j runs from, as soon as j is queued: its runner reads it as each stage
// starts, well after. One that could not be written is said (say), and the rerun runs every stage whole.
func (a *rerunAsk) save(s *state.Store, j state.Job) {
	a.saved = s.SaveRerun(j.ID, a.record)
}

// say tells the model what rerun j carries, narrows and runs whole.
func (a rerunAsk) say(io IO, s *state.Store, j state.Job) {
	if a.saved != nil {
		fmt.Fprintf(io.Out, "baton: note — the record %s runs from could not be written (%v), so it carries nothing and runs every stage whole.\n", j.ID, a.saved)
		return
	}
	v := state.PlannedRerun(j, a.r, a.record)
	v.ChangesIn(s.Root)
	fmt.Fprintln(io.Out, "baton: "+decide.RerunQueued(j.Phase, v))
}
