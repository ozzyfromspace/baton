// Package state keeps baton's files under .baton/ and serializes every change to them. A project (see
// Project) holds one run per session, and a run's directory holds:
//
//	plan.json     the attached plan (see package plan)
//	state.json    progress and the host's runtime state machine
//	events.jsonl  one line per decision baton makes: the audit trail
//	handoff.md    notes the model leaves for the phases after it (a worker's go to its parent run's)
//	jobs/<id>.log the output of each job (package lane)
//	gen-<N>/      the record of an earlier plan of the run, kept as it was replaced (Archive)
//	replan/       the plan's text as approved, each replan's, and an open replan's draft (replan.go)
//	lock          held (flock) for the duration of every update, and naming its holder meanwhile (lock.go)
//
// Hooks, the host and the model-facing CLI are separate processes that touch the same files, so every
// read-modify-write goes through Update, which holds an exclusive lock and replaces state.json atomically.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// Store is a handle on one run's directory.
type Store struct {
	Dir string
	// Root is the working tree the run works in: the project's checkout, or a worker's own worktree.
	// Everything about the phase's work in git reads it.
	Root     string
	Checkout string // the project's checkout, where its lanes and its .baton directory belong
	Project  string // the project's .baton directory
	Ref      string // the run's ref (see Project)
	Parent   string // a worker's sub-run: the ref of the run it works for ("" otherwise)
	// Agent is the phase agent whose hook opened its sub-run (LinkAgent): that agent's hooks are the
	// sub-run's own, while the lead's screen, where its dialogs show, is the parent run's.
	Agent    string
	Instance string // BATON_INSTANCE of the writer, recorded on events
	Now      func() time.Time
}

// Open returns a store for dir, creating it if needed. Its working tree is dir's parent: the shape of a
// project's .baton directory before runs were kept per session (see Project for where runs live now).
func Open(dir, instance string, now func() time.Time) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	root := filepath.Dir(dir)
	return &Store{Dir: dir, Root: root, Checkout: root, Project: filepath.Join(root, DirName), Ref: filepath.Base(dir), Instance: instance, Now: now}, nil
}

// Worker returns the phase a worker's sub-run works on, or "" for a run of its own.
func (s *Store) Worker() string {
	if s.Parent == "" {
		return ""
	}
	return strings.TrimPrefix(s.Ref, s.Parent+"/")
}

// ParentRun returns the run a worker's sub-run works for, writing as the same instance.
func (s *Store) ParentRun() (*Store, error) {
	if s.Parent == "" {
		return nil, fmt.Errorf("run %s is not a worker's", s.Ref)
	}
	return &Store{Dir: filepath.Join(s.Project, runsDir, s.Parent), Root: s.Checkout, Checkout: s.Checkout, Project: s.Project,
		Ref: s.Parent, Instance: s.Instance, Now: s.Now}, nil
}

// Relay queues r, a question of this worker's sub-run for the human, in the run it works for: the lead
// session puts it to them (QueueRelay). r.Phase is the worker's own phase. It takes the parent run's
// lock, so it is never called inside an update of the sub-run: no code holds two runs' locks at once.
// Queueing is idempotent, so the worker's commands and hooks may each queue the same question, and
// retry it.
func (s *Store) Relay(r Relay) error {
	up, err := s.ParentRun()
	if err != nil {
		return err
	}
	r.Phase = s.Worker()
	var queued bool
	if _, err := up.Update(func(st *State) error {
		if queued = QueueRelay(st, r, s.Now()); queued {
			st.Run.Announce = append(st.Run.Announce, "baton: the worker on "+r.Phase+" has a question for you ("+relayWhat[r.Kind]+"); this session puts it to you as soon as it can")
		}
		return nil
	}); err != nil {
		return err
	}
	if queued {
		fields := map[string]any{"phase": r.Phase, "type": r.Kind}
		if r.Decision != "" {
			fields["decision"] = r.Decision
		}
		up.Event("relay_queued", fields)
	}
	return nil
}

// Retook logs that this worker's sub-run took its phase up again at once (Retake), as verdict vd reopened
// it, by what found it (settle, its loop's tick, its stop), and leaves line for the human in the run it
// works for: nobody watches a phase agent's session. The parent run's lock is taken on its own, after the
// sub-run's update.
func (s *Store) Retook(vd Verdict, by, line string) {
	fields := map[string]any{"phase": vd.Phase, "reason": vd.Park, "by": by}
	if vd.Job.ID != "" {
		fields["job"] = vd.Job.ID
	}
	s.Event("phase_resumed", fields)
	if up, err := s.ParentRun(); err == nil {
		up.Update(func(st *State) error { st.Run.Announce = append(st.Run.Announce, line); return nil })
	}
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

// PlanPath is where plan.json lives.
func (s *Store) PlanPath() string { return s.path("plan.json") }

// HandoffPath is where the model's notes for later phases live. A worker's notes are its run's: the
// phases after its own read them there.
func (s *Store) HandoffPath() string {
	if s.Parent != "" {
		return filepath.Join(s.Project, runsDir, s.Parent, "handoff.md")
	}
	return s.path("handoff.md")
}

// JobLog is where job id's output goes.
func (s *Store) JobLog(id string) string { return filepath.Join(s.Dir, "jobs", id+jobLogExt) }

// JobStages is job id's stages file (lane.ReadStages), beside its log.
func (s *Store) JobStages(id string) string { return filepath.Join(s.Dir, "jobs", id+jobStagesExt) }

// The files a job keeps in jobs/: its log, its stages file, and a rerun's record (JobRerun).
const (
	jobLogExt    = ".log"
	jobStagesExt = ".stages"
	jobRerunExt  = ".rerun"
)

// JobFileID is the job whose file in jobs/ name is, or "" for a file no job keeps.
func JobFileID(name string) string {
	for _, ext := range []string{jobLogExt, jobStagesExt, jobRerunExt} {
		if id, ok := strings.CutSuffix(name, ext); ok && id != "" {
			return id
		}
	}
	return ""
}

// withLock runs fn holding the run's lock (see withLockFile), logging what the lock tells it in the run's
// events.
func (s *Store) withLock(fn func() error) error {
	return withLockFile(s.path("lock"), LockWatch{Now: s.Now, Event: func(kind string, fields map[string]any) { s.Event(kind, fields) }}, fn)
}

// Load reads state.json; a missing file is an empty, idle state.
func (s *Store) Load() (State, error) {
	var st State
	err := s.withLock(func() error {
		var err error
		st, err = s.read()
		return err
	})
	return st, err
}

// Peek reads state.json without the lock. Writes replace the file whole, so it is never torn; it may
// be a moment old, which is fine for a listing.
func (s *Store) Peek() (State, error) { return s.read() }

func (s *Store) read() (State, error) {
	st, _, err := s.readRaw()
	return st, err
}

// readRaw reads state.json, returning also the bytes it read: nil for a missing file.
func (s *Store) readRaw() (State, []byte, error) {
	b, err := os.ReadFile(s.path("state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil, nil
	}
	if err != nil {
		return State{}, nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, nil, fmt.Errorf("state.json: %w", err)
	}
	if st.Phases == nil {
		st.Phases = map[string]*PhaseState{}
	}
	return st, b, nil
}

// Update applies fn to the current state under the lock and saves the result atomically. If fn
// returns an error nothing is written, and neither is a state fn left as it was: an update that
// changes nothing leaves state.json alone, its modification time included (the time a listing shows
// as when the run last changed, and by which Abandoned judges a run quiet).
func (s *Store) Update(fn func(*State) error) (State, error) {
	var st State
	err := s.withLock(func() error {
		var (
			old []byte
			err error
		)
		if st, old, err = s.readRaw(); err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		if b = append(b, '\n'); old != nil && bytes.Equal(b, old) {
			return nil
		}
		return writeAtomic(s.path("state.json"), b)
	})
	return st, err
}

// Event appends one line to events.jsonl. Events are the audit trail: tests and humans read them.
// It takes no lock, so it may be called from inside an Update callback.
func (s *Store) Event(kind string, fields map[string]any) error {
	rec := map[string]any{}
	for k, v := range fields {
		rec[k] = v
	}
	// The reserved keys always win, so a field can never disguise one event as another.
	rec["ts"], rec["kind"] = s.Now().UTC().Format(time.RFC3339Nano), kind
	if s.Instance != "" {
		rec["instance"] = s.Instance
	}
	if w := s.Worker(); w != "" {
		rec["worker"] = w
		if s.Agent != "" {
			rec["agent"] = s.Agent
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return appendLine(s.path("events.jsonl"), append(b, '\n'))
}

// SavePlan writes plan.json atomically. Like Load and Update it takes the lock: never call it from
// inside an Update callback.
func (s *Store) SavePlan(p plan.Plan) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return s.withLock(func() error { return writeAtomic(s.PlanPath(), append(b, '\n')) })
}

// WritePlan writes plan.json from inside an Update callback, which already holds the lock that SavePlan
// takes: a plan attached or revised in the same step as the state that goes with it.
func (s *Store) WritePlan(p plan.Plan) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.PlanPath(), append(b, '\n'))
}

// LoadPlan reads plan.json.
func (s *Store) LoadPlan() (plan.Plan, error) { return plan.Load(s.PlanPath()) }

// AppendHandoff adds the model's notes for a phase to handoff.md.
func (s *Store) AppendHandoff(phase, notes string) error {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return nil
	}
	entry := fmt.Sprintf("## %s — %s\n\n%s\n\n", phase, s.Now().Format("2006-01-02 15:04"), notes)
	return appendLine(s.HandoffPath(), []byte(entry))
}

// appendLine appends data with a single write in append mode, which is atomic on POSIX and Windows for
// records this small. It deliberately takes no lock, so it is safe to call from inside Update.
func appendLine(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Close())
}

// writeAtomic replaces path with data so readers never see a partial file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := syncFile(tmp); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return rename(name, path)
}
