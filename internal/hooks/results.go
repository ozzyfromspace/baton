package hooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The results of the jobs the model handed to baton (baton verify, baton await) reach it through whichever
// hook comes first once one is due (state.Due): the next tool call's PostToolUse, the Stop that ends its
// turn, or, when the session is idle, the UserPromptSubmit of the line the host types to wake it. Each is
// given once. A phase that was set aside gets its result in the brief it resumes from.

// What a result carries of a job's log: its end, and at most resultFailures of the lines before it that
// report a failure, each clipped to resultClip bytes.
const (
	resultLines    = 80
	resultBytes    = 6 << 10
	resultFailures = 20
	resultClip     = 200
)

// delivery is the results a hook took for the model under the state's lock; the logs are read after.
type delivery struct {
	jobs     []state.Job
	red      []state.Job // the phases red now (state.Red)
	complete bool        // the plan is complete: these results are the last of it
	looks    lookIns     // the look-ins taken with them (takeLookIns)
}

// take takes the results due (state.TakeDue), and the look-ins due beside them (takeLookIns).
func take(st *state.State, now time.Time) delivery {
	looks := takeLookIns(st, now)
	jobs := state.TakeDue(st, now)
	if len(jobs) == 0 {
		return delivery{looks: looks}
	}
	return delivery{jobs: jobs, red: st.Red(), complete: st.Mode == state.ModeComplete, looks: looks}
}

// takeLookIns takes the look-ins due to this store's session, whatever phase the run is on: the first
// failures of its gates that run on (state.UntoldFailures, marked FailTold), and what the host left for it
// (state.LookIn). Every path a result takes gives them: a tool call, a stop, a hand-back, a prompt (one
// the host types to an idle session among them, Loop.deliver) and a brief.
func takeLookIns(st *state.State, now time.Time) lookIns {
	failing, left := state.TakeLookIns(st, now)
	return lookIns{failing: failing, left: left}
}

func (d delivery) any() bool { return len(d.jobs) > 0 || d.looks.any() }

// text is the results for the model, each with the end of its log. aggregate is aggregate_qa (jobResult).
func (d delivery) text(s *state.Store, aggregate bool) string {
	pl, _ := s.LoadPlan()
	var parts []string
	for _, j := range d.jobs {
		parts = append(parts, jobResult(s, pl, j, d.red, aggregate))
	}
	if d.looks.any() {
		parts = append(parts, d.looks.text(s))
	}
	if d.complete {
		// The run's decisions, its phase agents' too: read without the lock, which no code holds twice.
		st, _ := s.Load()
		parts = append(parts, decide.VerifiedComplete(s.Decisions(st)))
	}
	return strings.Join(parts, "\n\n")
}

// says tells the human which results the model was given.
func (d delivery) says() string {
	var lines []string
	for _, j := range d.jobs {
		lines = append(lines, fmt.Sprintf("baton: job %s (`%s`, %s) %s → told Claude", j.ID, j.Command, j.Phase, decide.Outcome(j)))
	}
	if d.looks.any() {
		lines = append(lines, d.looks.says())
	}
	return strings.Join(lines, "\n")
}

// event logs the delivery, and how it went.
func (d delivery) event(s *state.Store, via string) {
	d.looks.event(s, via)
	if len(d.jobs) == 0 {
		return
	}
	var ids []string
	for _, j := range d.jobs {
		ids = append(ids, j.ID)
	}
	s.Event("results_delivered", map[string]any{"jobs": ids, "via": via})
}

// jobResult is one job's result for the model (decide.JobResult), with what it shows of its log (JobLog).
// With aggregate (aggregate_qa), a red result of a phase with a QA phase ahead in pl says other phases' work
// may be why.
func jobResult(s *state.Store, pl plan.Plan, j state.Job, red []state.Job, aggregate bool) string {
	qa := ""
	if aggregate {
		qa = pl.QAAhead(j.Phase)
	}
	return decide.JobResult(j, JobLog(s, j), red, qa)
}

// JobLog reads what a result shows of job j's log: its end, and the lines before that end that report a
// failure, so that one early in a long log is never lost behind it; and, for a rerun of a red gate, what
// it carried and narrowed (state.Store.Reran, which runs git).
func JobLog(s *state.Store, j state.Job) decide.Log {
	path := j.Log
	if path == "" {
		path = s.JobLog(j.ID)
	}
	e, _ := lane.ReadExcerpt(path, resultLines, resultBytes, resultFailures, resultClip)
	return decide.Log{Path: path, Tail: e.Tail, Failures: e.Failures, More: e.More, Rerun: s.Reran(j), Env: jobEnv(s, j, path)}
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

// news is what else a hook took for the model under the state's lock: baton's messages (state.Message)
// and, in the lead, the work that landed underneath it (state.Landing) and the records baton committed
// to the plan's record file (state.RecordNote).
type news struct {
	text     string
	mail     []state.Message
	landings []state.Landing
	records  []state.RecordNote
}

// takeNews takes the messages, landings and records due (state.TakeMail, state.TakeLandings,
// state.TakeRecords). Nothing wakes the lead for a record: it hears of it with its next tool call or prompt.
func takeNews(st *state.State, s *state.Store) news {
	var n news
	var parts []string
	for _, m := range state.TakeMail(st) {
		n.mail = append(n.mail, m)
		parts = append(parts, m.Text)
	}
	if s.Worker() == "" {
		if n.landings = state.TakeLandings(st); len(n.landings) > 0 {
			parts = append(parts, decide.Landed(n.landings, st.RunBranch))
		}
		if n.records = state.TakeRecords(st); len(n.records) > 0 {
			parts = append(parts, decide.RecordsTold(n.records))
		}
	}
	n.text = strings.Join(parts, "\n\n")
	return n
}

func (n news) any() bool { return n.text != "" }

// event logs what the model was told, and how.
func (n news) event(s *state.Store, via string) {
	for _, m := range n.mail {
		s.Event("message_delivered", map[string]any{"message": m.Kind, "via": via})
	}
	if len(n.landings) > 0 {
		var phases []string
		for _, l := range n.landings {
			phases = append(phases, l.Phase)
		}
		s.Event("landed_told", map[string]any{"phases": phases, "via": via})
	}
	if len(n.records) > 0 {
		var phases []string
		for _, r := range n.records {
			phases = append(phases, r.Phase)
		}
		s.Event("record_told", map[string]any{"phases": phases, "via": via})
	}
}
