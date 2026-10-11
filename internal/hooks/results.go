package hooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The results of the jobs the model handed to baton (baton verify, baton await) reach it through whichever
// hook comes first once one is due (state.Due): the next tool call's PostToolUse, the Stop that ends its
// turn, or, when the session is idle, the UserPromptSubmit of the line the host types to wake it. Each is
// given once. A phase that was set aside gets its result in the brief it resumes from.

// The end of a job's log a result carries.
const (
	resultLines = 80
	resultBytes = 6 << 10
)

// delivery is the results a hook took for the model under the state's lock; the logs are read after.
type delivery struct {
	jobs      []state.Job
	red       []state.Job // the phases red now (state.Red)
	complete  bool        // the plan is complete: these results are the last of it
	decisions int         // the decisions the run made without the human
}

// take takes the results due (state.TakeDue).
func take(st *state.State, now time.Time) delivery {
	jobs := state.TakeDue(st, now)
	if len(jobs) == 0 {
		return delivery{}
	}
	return delivery{jobs: jobs, red: st.Red(), complete: st.Mode == state.ModeComplete, decisions: len(decide.WithoutHuman(st.Decisions))}
}

func (d delivery) any() bool { return len(d.jobs) > 0 }

// text is the results for the model, each with the end of its log.
func (d delivery) text(s *state.Store) string {
	var parts []string
	for _, j := range d.jobs {
		parts = append(parts, jobResult(s, j, d.red))
	}
	if d.complete {
		parts = append(parts, decide.VerifiedComplete(d.decisions))
	}
	return strings.Join(parts, "\n\n")
}

// says tells the human which results the model was given.
func (d delivery) says() string {
	var lines []string
	for _, j := range d.jobs {
		lines = append(lines, fmt.Sprintf("baton: job %s (`%s`, %s) %s → told Claude", j.ID, j.Command, j.Phase, decide.Outcome(j)))
	}
	return strings.Join(lines, "\n")
}

// event logs the delivery, and how it went.
func (d delivery) event(s *state.Store, via string) {
	var ids []string
	for _, j := range d.jobs {
		ids = append(ids, j.ID)
	}
	s.Event("results_delivered", map[string]any{"jobs": ids, "via": via})
}

// jobResult is one job's result for the model (decide.JobResult), with the end of its log.
func jobResult(s *state.Store, j state.Job, red []state.Job) string {
	log := j.Log
	if log == "" {
		log = s.JobLog(j.ID)
	}
	tail, _ := lane.Tail(log, resultLines, resultBytes)
	return decide.JobResult(j, log, tail, red)
}

// announced takes the lines the host left for the human (Runtime.Announce).
func announced(st *state.State) string {
	lines := st.Run.Announce
	st.Run.Announce = nil
	return strings.Join(lines, "\n")
}

// waitingOn names the jobs a run with no phase in hand waits for.
func waitingOn(jobs []state.Job) string {
	var names []string
	for _, j := range jobs {
		names = append(names, fmt.Sprintf("%s (`%s`, %s)", j.ID, j.Command, j.Phase))
	}
	if len(names) == 1 {
		return "job " + names[0]
	}
	return "jobs " + strings.Join(names, ", ")
}

// lines joins the non-empty lines of a message.
func lines(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
