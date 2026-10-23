package state

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
)

// A rerun (`baton verify --failed`) runs a red gate's command again on what failed in it alone: the stages
// that passed are carried, each failed stage whose failures are known runs narrowed to them, and every other
// stage runs whole (the gate protocol, `baton stage`). It has the red gate's command, so it is the same
// check (Same), and it takes over from the red gate (takesOver, Job.Reruns): a pass clears it, a failure is
// the phase's red gate from then on. What it carries and narrows comes from baton's record of the gate's
// stages, never from a model's reading of a log (v0.8.4 shipped a regression that way).
//
// The record a rerun runs from is written beside its log as it is queued (Store.JobRerun), so that what it
// carries is fixed then: it is the gate's stages file the rerun's command gets (lane.EnvRerun).

// Why a phase has nothing for --failed to rerun (RerunOf).
const (
	RerunNoGate   = "no red gate"
	RerunNoStages = "no stages"
)

// Rerun is what `baton verify --failed` reruns for a phase (RerunOf).
type Rerun struct {
	// Of is the gate it reruns (Job.Reruns): the phase's red gate, or, with none red, its latest gate that
	// was stopped, or runs on, after a failure.
	Of Job
	// From are the gates whose records it draws on, oldest first: the red gate, then the later runs of its
	// command that ended with no verdict (stopped, say) after showing a failure, or run now and are about to
	// be replaced; with no red gate, such runs of Of's command since the phase's last pass, Of the newest.
	From []Job
	// Stages is the record it runs from: each stage's latest record among From, in the order they first
	// ran. A stage that passed names the job it passed in (From) and the commit (Commit).
	Stages []lane.Stage
}

// RerunOf is what `baton verify --failed` reruns for phase, with each job's stages as stagesOf reads them
// (Store.StagesOf, read before the run's lock). It is the phase's red gate (RedGate), drawing too on the
// later runs of its command that showed a failure and ended without a verdict, or run now (a gate stopped
// at its first failure, with `baton stop`, is the common one): their records are newer. With no red gate,
// the phase's latest gate since its last pass that showed a failure and has no verdict stands in for one.
// why is RerunNoGate when there is none of these, and RerunNoStages when they recorded no stages.
func (st State) RerunOf(phase string, stagesOf func(Job) []lane.Stage) (Rerun, string) {
	g := st.RedGate(phase)
	start := 0
	if g != nil {
		start = slices.IndexFunc(st.Jobs, func(j Job) bool { return j.ID == g.ID }) + 1
	} else {
		for i, j := range st.Jobs {
			if j.Phase == phase && j.kind() == JobVerify && j.Status == JobPassed {
				start = i + 1
			}
		}
	}
	var failed []Job // the later gates with no verdict (yet) that showed a failure, oldest first
	for _, j := range st.Jobs[start:] {
		if j.Phase == phase && j.kind() == JobVerify && !j.Red() && j.Status != JobPassed && sawFailure(j, stagesOf(j)) {
			failed = append(failed, j)
		}
	}
	var r Rerun
	switch {
	case g != nil:
		r.Of, r.From = *g, []Job{*g}
	case len(failed) > 0:
		r.Of = failed[len(failed)-1]
	default:
		return Rerun{}, RerunNoGate
	}
	for _, j := range failed {
		if j.Command == r.Of.Command {
			r.From = append(r.From, j)
		}
	}
	if r.Stages = mergeStages(r.From, stagesOf); len(r.Stages) == 0 {
		return r, RerunNoStages
	}
	return r, ""
}

// sawFailure reports a gate that showed a failure: a failure line or marker seen as it ran (Failing), or a
// stage that failed.
func sawFailure(j Job, stages []lane.Stage) bool {
	return j.Failing != nil || slices.ContainsFunc(stages, func(s lane.Stage) bool { return s.Status == lane.StageFailed })
}

// mergeStages is the latest record of each stage among jobs, oldest first, in the order the stages first
// ran. Each names the job its record comes from (From) and the commit: the job it ran in, or, carried, the
// job it passed in, since the record is read by `baton stage` from a file of the rerun's own (JobRerun). A
// stage a later job showed a failure in that never ended has no record any more: what an earlier job knew
// of it is stale, so a rerun runs it whole.
func mergeStages(jobs []Job, stagesOf func(Job) []lane.Stage) []lane.Stage {
	var out []lane.Stage
	for _, j := range jobs {
		recs := stagesOf(j)
		for _, s := range recs {
			if s.Commit == "" {
				s.Commit = j.Commit
			}
			if s.From == "" {
				s.From = j.ID
			}
			if i := slices.IndexFunc(out, func(o lane.Stage) bool { return o.Name == s.Name }); i >= 0 {
				out[i] = s
			} else {
				out = append(out, s)
			}
		}
		if f := j.Failing; f != nil && f.Stage != "" && !slices.ContainsFunc(recs, func(s lane.Stage) bool { return s.Name == f.Stage }) {
			out = slices.DeleteFunc(out, func(o lane.Stage) bool { return o.Name == f.Stage })
		}
	}
	return out
}

// Unrecorded lists the names among names that are no stage of stages.
func Unrecorded(stages []lane.Stage, names []string) []string {
	var out []string
	for _, n := range names {
		if !slices.ContainsFunc(stages, func(s lane.Stage) bool { return s.Name == n }) {
			out = append(out, n)
		}
	}
	return out
}

// RerunRecord is the record a rerun runs from (Store.JobRerun): stages, but for those named in again, which
// it runs whole (--stages): the carried stages they bring back, or failed ones run whole rather than
// narrowed.
func RerunRecord(stages []lane.Stage, again []string) []lane.Stage {
	return slices.DeleteFunc(slices.Clone(stages), func(s lane.Stage) bool { return slices.Contains(again, s.Name) })
}

// Carries reports whether a rerun carries stage s of its record: it passed there, or was carried there in
// turn.
func Carries(s lane.Stage) bool { return s.Status == lane.StagePassed || s.Status == lane.StageCarried }

// Narrows reports whether a rerun runs stage s of its record narrowed to its failures: it failed with
// failures known.
func Narrows(s lane.Stage) bool { return s.Status == lane.StageFailed && len(s.Failures) > 0 }

// StagesOf is job j's stages as recorded: its stages file (lane.ReadStages), which `baton stage` writes,
// or, without one, the stages the runner read from its log (Job.Stages).
func (s *Store) StagesOf(j Job) []lane.Stage {
	if stages, _ := lane.ReadStages(s.JobStages(j.ID)); len(stages) > 0 {
		return stages
	}
	return j.Stages
}

// JobRerun is the record rerun id runs from (RerunRecord), beside its log: the stages file its command
// gets as the red gate's (lane.EnvRerun). A rerun whose record is missing carries nothing, and runs whole.
func (s *Store) JobRerun(id string) string { return filepath.Join(s.Dir, "jobs", id+jobRerunExt) }

// SaveRerun writes the record rerun id runs from (JobRerun), whole, as a stages file.
func (s *Store) SaveRerun(id string, record []lane.Stage) error {
	var b bytes.Buffer
	for _, st := range record {
		line, err := json.Marshal(st)
		if err != nil {
			return err
		}
		b.Write(append(line, '\n'))
	}
	path := s.JobRerun(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, b.Bytes())
}

// Reran is what rerun j's result says it carried and narrowed (RanRerun), from its stages as recorded and
// the record it ran from, with the files changed since each carried stage passed. It runs git, so it is
// called outside any lock. nil for a job that reruns nothing.
func (s *Store) Reran(j Job) *RerunView {
	if j.Reruns == "" {
		return nil
	}
	record, _ := lane.ReadStages(s.JobRerun(j.ID))
	v := RanRerun(j, s.StagesOf(j), record)
	v.ChangesIn(s.Root)
	return &v
}

// RerunView is what a rerun carries and narrows, for the words that tell of it (decide): as queued, from
// the record it runs from (Planned), or as it ran (Ran).
type RerunView struct {
	Of     string // the gate it reruns
	Commit string // the commit it checks
	// Carried are the stages it does not run again, as they passed: the commit (Commit) and the job (From),
	// each with the files changed since, to Commit.
	Carried []CarriedStage
	// Narrowed are the stages run narrowed to the failures they had (Failures).
	Narrowed []lane.Stage
	// Whole are the stages run whole, each with why and its last time (WholeStage): those of the gate's
	// record brought back (--stages), failed with no failures known, or skipped, and those a gate it draws on
	// showed a failure in and never ended. Any other stage not on record runs whole too. Only as queued.
	Whole []WholeStage
}

// CarriedStage is a stage a rerun carries, with the files changed since the commit it passed on.
type CarriedStage struct {
	lane.Stage
	Changed []string // at most MaxChanged
	More    int      // how many more changed
	Known   bool     // the files changed since were read: none means none
}

// MaxChanged is how many of the files changed since a carried stage passed a rerun's words name.
const MaxChanged = 20

// PlannedRerun is what rerun j of r will carry, narrow and run whole, running from record (RerunRecord): as
// it is queued.
func PlannedRerun(j Job, r Rerun, record []lane.Stage) RerunView {
	v := RerunView{Of: r.Of.ID, Commit: j.Commit}
	for _, s := range r.Stages {
		at := slices.IndexFunc(record, func(o lane.Stage) bool { return o.Name == s.Name })
		switch {
		case at >= 0 && Carries(record[at]):
			v.Carried = append(v.Carried, CarriedStage{Stage: record[at]})
		case at >= 0 && Narrows(record[at]):
			v.Narrowed = append(v.Narrowed, record[at])
		default:
			v.Whole = append(v.Whole, wholeStage(s, at >= 0))
		}
	}
	v.Whole = append(v.Whole, unrecorded(r.From, r.Stages)...)
	return v
}

// RanRerun is what rerun j carried and narrowed as it ran: its stages as recorded (ran), against the record
// it ran from (record).
func RanRerun(j Job, ran, record []lane.Stage) RerunView {
	v := RerunView{Of: j.Reruns, Commit: j.Commit}
	for _, s := range ran {
		at := slices.IndexFunc(record, func(o lane.Stage) bool { return o.Name == s.Name })
		switch {
		case s.Status == lane.StageCarried:
			if at >= 0 && s.Commit == "" {
				s.Commit = record[at].Commit
			}
			if at >= 0 && s.From == "" {
				s.From = record[at].From
			}
			v.Carried = append(v.Carried, CarriedStage{Stage: s})
		case at >= 0 && Narrows(record[at]) && (s.Status == lane.StagePassed || s.Status == lane.StageFailed):
			s.Failures = record[at].Failures
			v.Narrowed = append(v.Narrowed, s)
		}
	}
	return v
}

// Changes fills in the files changed since each carried stage's commit, to v's, as changed lists them
// (gitx.Changes: run it outside any lock). A stage whose commit is v's has none; one changed cannot list is
// left unknown.
func (v *RerunView) Changes(changed func(from, to string) ([]string, error)) {
	type diff struct {
		files []string
		err   error
	}
	seen := map[string]diff{}
	for i := range v.Carried {
		c := &v.Carried[i]
		if c.Commit == "" || v.Commit == "" || c.Commit == v.Commit {
			continue
		}
		d, ok := seen[c.Commit]
		if !ok {
			d.files, d.err = changed(c.Commit, v.Commit)
			seen[c.Commit] = d
		}
		if d.err != nil {
			continue
		}
		files := d.files
		c.Changed, c.More, c.Known = files, 0, true
		if len(files) > MaxChanged {
			c.Changed, c.More = files[:MaxChanged], len(files)-MaxChanged
		}
	}
}

// ChangesIn fills in the files changed since each carried stage passed (Changes) from the repository at
// root. It runs git: call it outside any lock.
func (v *RerunView) ChangesIn(root string) {
	v.Changes(func(from, to string) ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), changesWait)
		defer cancel()
		changes, err := gitx.Changes(ctx, root, from, to)
		var files []string
		for _, c := range changes {
			files = append(files, c.Path)
		}
		return files, err
	})
}

// changesWait is how long ChangesIn waits for git to list the files changed since a commit: a result that
// lists them is given in a hook.
const changesWait = 5 * time.Second

// StopJob stops job id, queued or running, as `baton stop` asks, for reason: it is marked interrupted (with
// no verdict, so a red gate before it stands as it was), its result needs no telling, and its runner ends
// its process group at its next beat, finding it no longer wanted. It returns the job as it was, reporting
// whether it stopped it: false when it is not on record, or has ended.
func StopJob(st *State, id, reason string, now time.Time) (Job, bool) {
	stopped := CancelJobs(st, func(j Job) bool { return j.ID == id }, reason, now)
	if len(stopped) == 0 {
		return Job{}, false
	}
	return stopped[0], true
}
