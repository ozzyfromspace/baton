package cli

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A repeat states its reason (v0.8.11, decide/why.go): `--why` answers baton's questions about a gate, so it
// is queued, and the human is told the reason; a long repeat queued again with none is queued all the same,
// flagged to the human and in its result.

// failuresOf reads the tests a red job j's log says failed (lane.FailureIDs): at most decide.GateListed,
// and how many more. None for a job that is not red, or a log that cannot be read.
func failuresOf(s *state.Store, j state.Job) ([]lane.FailedTest, int) {
	if !j.Red() {
		return nil, 0
	}
	log := j.Log
	if log == "" {
		log = s.JobLog(j.ID)
	}
	fs, more, err := lane.FailureIDs(log, 0)
	if err != nil {
		return nil, 0
	}
	n := min(len(fs), decide.GateListed)
	return fs[:n], more + len(fs) - n
}

// gateSaid is what the human is told of a gate queued over baton's questions (gateAsk.said).
type gateSaid struct {
	line  string // for the human, "" for nothing
	event string // gate_repeated, gate_unexplained, or "" (job_queued's why says it)
}

// said is what the human is told of gate command, queued for phase with why (--why, "" for none) once
// baton's questions about it are asked: the reason, with --why, for every kind of question; for a long
// repeat queued again without one, that it ran unexplained, which its result opens with too.
func (a gateAsk) said(phase, command, why string) gateSaid {
	switch {
	case why != "" && a.repeat:
		return gateSaid{decide.RepeatSaid(phase, a.took, a.cost, why), "gate_repeated"}
	case why != "" && len(a.applies) > 0:
		return gateSaid{decide.GateAnswered(phase, command, a.applies, why), ""}
	case why == "" && a.repeat:
		return gateSaid{decide.RepeatUnexplained(phase, a.took, a.cost), "gate_unexplained"}
	}
	return gateSaid{}
}

// logged logs what the human was told of job, which a's questions were about.
func (g gateSaid) logged(s *state.Store, a gateAsk, job state.Job) {
	if g.event == "" {
		return
	}
	ev := map[string]any{"job": job.ID, "phase": job.Phase, "command": job.Command, "commit": job.Commit,
		"last": a.job, "took_seconds": int(a.took / time.Second), "run": a.cost.Command + 1,
		"so_far_seconds": int(a.cost.CommandTime / time.Second), "phase_seconds": int(a.cost.Phase / time.Second)}
	if job.Why != "" {
		ev["why"] = job.Why
	}
	s.Event(g.event, ev)
}

// tellLead tells the human line in the lead's session when s is a phase agent's run, which nobody watches;
// the lead's own run has it already, with its queued line.
func tellLead(s *state.Store, line string) {
	if up, err := s.ParentRun(); err == nil {
		up.Update(func(st *state.State) error {
			st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: %s", line))
			return nil
		})
	}
}
