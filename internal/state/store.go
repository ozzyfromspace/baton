// Package state keeps baton's files under .baton/ and serializes every change to them. A project (see
// Project) holds one run per session, and a run's directory holds:
//
//	plan.json     the attached plan (see package plan)
//	state.json    progress and the host's runtime state machine
//	events.jsonl  one line per decision baton makes: the audit trail
//	handoff.md    notes the model leaves for the phases after it
//	lock          held (flock) for the duration of every update
//
// Hooks, the host and the model-facing CLI are separate processes that touch the same files, so every
// read-modify-write goes through Update, which holds an exclusive lock and replaces state.json atomically.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/ozzyfromspace/baton/internal/plan"
)

// Store is a handle on one run's directory.
type Store struct {
	Dir      string
	Root     string // the working tree the run works in
	Instance string // BATON_INSTANCE of the writer, recorded on events
	Now      func() time.Time
}

// Open returns a store for dir, creating it if needed. Its working tree is dir's parent: the shape of a
// project's .baton directory before runs were kept per session (see Project for where runs live now).
func Open(dir, instance string, now func() time.Time) (*Store, error) {
	return openIn(dir, filepath.Dir(dir), instance, now)
}

func openIn(dir, root, instance string, now func() time.Time) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir, Root: root, Instance: instance, Now: now}, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

// PlanPath is where plan.json lives.
func (s *Store) PlanPath() string { return s.path("plan.json") }

// HandoffPath is where the model's notes for later phases live.
func (s *Store) HandoffPath() string { return s.path("handoff.md") }

// lockTimeout bounds how long any process waits for another's update; updates take milliseconds.
const lockTimeout = 5 * time.Second

func (s *Store) withLock(fn func() error) error { return withLockFile(s.path("lock"), fn) }

// withLockFile runs fn holding an exclusive lock on path.
func withLockFile(path string, fn func() error) error {
	l := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	ok, err := l.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !ok {
		return fmt.Errorf("baton state is locked by another process (%v)", err)
	}
	defer l.Unlock()
	return fn()
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

// peek reads state.json without the lock. Writes replace the file whole, so it is never torn; it may
// be a moment old, which is fine for a listing.
func (s *Store) peek() (State, error) { return s.read() }

func (s *Store) read() (State, error) {
	b, err := os.ReadFile(s.path("state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("state.json: %w", err)
	}
	if st.Phases == nil {
		st.Phases = map[string]*PhaseState{}
	}
	return st, nil
}

// Update applies fn to the current state under the lock and saves the result atomically. If fn
// returns an error nothing is written.
func (s *Store) Update(fn func(*State) error) (State, error) {
	var st State
	err := s.withLock(func() error {
		var err error
		if st, err = s.read(); err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(s.path("state.json"), append(b, '\n'))
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
