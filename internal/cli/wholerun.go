package cli

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole rerun says why and what it costs (v0.8.15, decide/wholerun.go): a --failed rerun that would run
// whole a red stage that took decide.GateLong or more last time is questioned once per command and commit,
// as a gate is (questionGate), before it is queued. --why answers it, the reason told to the human; the same
// call again with none is queued too, flagged to the human and in its result, as a long repeat is (why.go).

// questionWhole is what baton asks of rerun a, about to be queued on commit for phase, from st, the run's
// state read before its lock: a red stage it runs whole that took decide.GateLong or more last time
// (decide.GateWholeRerun). Nothing for a narrowed rerun, which runs the command given.
func (a rerunAsk) questionWhole(io IO, s *state.Store, st state.State, phase, commit string) gateAsk {
	var g gateAsk
	if a.narrow != nil {
		return g
	}
	v := state.PlannedRerun(state.Job{Commit: commit}, a.r, a.record)
	long, from := longRed(v.Whole, a.r.Stages)
	if len(long) == 0 {
		return g
	}
	command := a.r.Of.Command
	g.whole, g.applies, g.job = long, []string{decide.GateWholeRerun}, a.r.Of.ID
	for _, w := range long {
		g.took += w.Took
	}
	g.cost = state.GateCost(st, phase, command, io.Now())
	if key := gateKey(decide.GateWholeRerun, command, commit); !slices.Contains(st.Run.GatesQuestioned, key) {
		var logs []string
		for _, id := range from {
			j := state.Job{ID: id}
			if x := st.Job(id); x != nil {
				j = *x
			}
			logs = append(logs, jobLog(s, j))
		}
		g.types, g.keys = []string{decide.GateWholeRerun}, []string{key}
		g.questions = []string{decide.WholeRerunQuestioned(phase, a.r.Of, long, logs, g.cost)}
	}
	return g
}

// longRed are the stages of whole that went red on record (stages, the record the rerun's gate merged) and
// took decide.GateLong or more last time, and the gates they went red in, each once.
func longRed(whole []state.WholeStage, stages []lane.Stage) (long []state.WholeStage, from []string) {
	for _, w := range whole {
		at := slices.IndexFunc(stages, func(s lane.Stage) bool { return s.Name == w.Name })
		if at < 0 || stages[at].Status != lane.StageFailed || w.Took < decide.GateLong {
			continue
		}
		long = append(long, w)
		if id := stages[at].From; id != "" && !slices.Contains(from, id) {
			from = append(from, id)
		}
	}
	return long, from
}

// refuseWhole records baton's question about a whole rerun as asked and puts it to the model: the rerun is
// not queued this time.
func (a gateAsk) refuseWhole(io IO, s *state.Store, phase, command, commit string) int {
	a.markAsked(s, phase, command, commit)
	return fail(io, "%s", a.questions[0])
}

// wholeSaid is what the human is told of a whole rerun queued over baton's question, with why (--why, ""
// for none): the reason, or that it ran unexplained, which its result opens with too.
func (a gateAsk) wholeSaid(phase, why string) gateSaid {
	if why != "" {
		return gateSaid{decide.WholeRerunSaid(phase, a.whole, a.cost, why), "gate_repeated"}
	}
	return gateSaid{decide.WholeRerunUnexplained(phase, a.whole, a.cost), "gate_unexplained"}
}

// wholeEvent adds to ev, an event about a whole rerun's question, the long red stages it runs whole and
// their last time together; nothing for another gate's.
func (a gateAsk) wholeEvent(ev map[string]any) {
	if len(a.whole) == 0 {
		return
	}
	names := make([]string, len(a.whole))
	for i, w := range a.whole {
		names[i] = w.Name
	}
	ev["whole"], ev["took_seconds"] = names, int(a.took/time.Second)
}
