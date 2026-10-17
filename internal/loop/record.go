package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A plan may name a record file in its standing rules (plan.Plan.Record). Once a phase is done (a lead
// phase) or lands (an agent's), baton appends the phase's record to it in the lead's checkout: its heading
// (## <id> — <title>), when, its `baton done` notes and its commits (decide.RecordEntry). Then it commits
// that file alone (gitx.CommitOnly: `git commit --only`), a new commit of the one path, so the lead's own
// work, staged or not, stays as it was. It is the one commit baton makes into a project, and only for a
// plan that names the file. Like a landing (merge.go), it holds the project's landing lock, and goes only
// while no tool call of a lead in the checkout runs, nor a gate a lead runs there; a checkout off the run's
// branch, or a record file with uncommitted changes, holds it (record_held), and nothing touches the file
// until it is clean again. Records are written one at a time, in the order the phases were done, each
// logged record_written; the lead's model is told of them at its next tool call or prompt
// (state.TakeRecords), never woken for one. No model edits the file (hooks' recordEdit).

// How often the record step may run, and how long it waits after the record file or the checkout held a
// record, or git failed.
const (
	recordEvery       = 2 * time.Second
	recordHeldRetry   = 10 * time.Second
	recordFailedRetry = time.Minute
)

// recorder is the record step's own state. Only the lead's tick touches at, and only a step said; retry is
// the step's, read by the tick.
type recorder struct {
	busy  atomic.Bool
	at    time.Time    // when the tick last looked for a record due
	retry atomic.Int64 // no step before then (Unix ns): the file or the checkout held a record, or git failed
	said  string       // the hold or failure told last, so each is told once
}

// records starts the record step, off the lead's tick, when a phase's record is due (state.Unrecorded):
// the run's plan names a record file, and a phase of it is done with none. A paused run writes nothing:
// the human has the wheel.
func (c *Coordinator) records(v host.View, s *state.Store, st state.State) {
	if c.Project == nil || s.Worker() != "" || st.Mode != state.ModeRunning && st.Mode != state.ModeComplete {
		return
	}
	if v.Now.Sub(c.rec.at) < recordEvery || v.Now.UnixNano() < c.rec.retry.Load() {
		return
	}
	c.rec.at = v.Now
	pl, err := s.LoadPlan()
	if err != nil || len(st.Unrecorded(pl)) == 0 || !c.rec.busy.CompareAndSwap(false, true) {
		return
	}
	c.fleetWG.Add(1)
	run := func(fn func()) { go fn() }
	if c.Fleet != nil {
		run = c.Fleet.run
	}
	run(func() {
		defer c.fleetWG.Done()
		defer c.rec.busy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				c.Lead.logf("loop: record: panic: %v", r)
				s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "record"})
			}
		}()
		c.record(v.Now, s)
	})
}

// record writes the record of the first phase due, if nothing holds it.
func (c *Coordinator) record(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil {
		return
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return
	}
	due := st.Unrecorded(pl)
	if len(due) == 0 {
		return
	}
	root, path, id := s.Checkout, pl.Record, due[0]
	usable := gitx.Usable(root)
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if usable && st.RunBranch != "" {
		if br := gitx.Branch(root); br != st.RunBranch {
			c.recordHeld(now, s, id, path, "off_branch", decide.RecordOffBranch(br, st.RunBranch))
			return
		}
	}
	entry := recordEntry(ctx, s, st, pl, id, usable).Text()

	// As a landing does: no tool call of a lead runs in the checkout, nor a gate there, and the landing lock
	// keeps the next one waiting until the record is committed.
	if c.busy(now, s) != "" || c.gate(s) != "" {
		return
	}
	release, ok, _ := state.HoldLanding(s.Project)
	if !ok {
		return
	}
	if c.busy(now, s) != "" || c.gate(s) != "" {
		release()
		return
	}
	commit, dirty, err := writeRecord(ctx, root, path, usable, recordAppender(pl.Title, entry), decide.RecordCommit(id, pl.Phases[pl.Index(id)].Title))
	release() // what the run's state records of it is written after: no lock is held under another
	switch {
	case dirty:
		c.recordHeld(now, s, id, path, "dirty", decide.RecordDirty(path))
	case err != nil:
		c.recordFailed(now, s, id, path, err)
	default:
		c.recorded(now, s, id, path, commit)
	}
}

// writeRecord appends to the record file, path in the checkout at root, what add makes of its content (nil
// when it holds the record already), and commits the file alone with message msg, returning the commit
// ("" with no git, usable false). A file with uncommitted changes is left alone (dirty). A write whose
// commit fails is taken back: the file is as it was, committed, and nothing of baton's stays staged.
func writeRecord(ctx context.Context, root, path string, usable bool, add func(old []byte, existed bool) []byte, msg string) (commit string, dirty bool, err error) {
	if usable {
		if dirty, err = gitx.PathDirty(root, path); dirty || err != nil {
			return "", dirty, err
		}
	}
	file := filepath.Join(root, filepath.FromSlash(path))
	old, err := os.ReadFile(file)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	content := add(old, existed)
	if content == nil {
		// Written and committed already, by a host that ended before the run's state recorded it.
		return gitx.LastCommitOf(ctx, root, path), false, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(file, content, 0o644); err != nil {
		return "", false, err
	}
	if !usable {
		return "", false, nil
	}
	if commit, err = gitx.CommitOnly(ctx, root, path, msg); err != nil {
		if existed {
			os.WriteFile(file, old, 0o644)
		} else if !gitx.Tracked(context.Background(), root, path) {
			os.Remove(file)
		}
		return "", false, err
	}
	return commit, false, nil
}

// recordAppender appends entry to a record file of the plan titled title (recordAppended), or gives nil
// for a file that ends with it already.
func recordAppender(title, entry string) func(old []byte, existed bool) []byte {
	return func(old []byte, existed bool) []byte {
		if existed && strings.HasSuffix(string(old), entry) {
			return nil
		}
		return recordAppended(old, existed, title, entry)
	}
}

// recordAppended is the record file's content with entry appended: a new file starts with a header for
// the plan titled title, and an entry is set off from what is before it by a blank line.
func recordAppended(old []byte, existed bool, title, entry string) []byte {
	text := string(old)
	switch {
	case !existed || strings.TrimSpace(text) == "":
		text = decide.RecordHeader(title)
	case strings.HasSuffix(text, "\n\n"):
	case strings.HasSuffix(text, "\n"):
		text += "\n"
	default:
		text += "\n\n"
	}
	return []byte(text + entry)
}

// recordEntry is phase id's record: its title, when it was done or landed, its `baton done` notes (the
// latest it left in the run's handoff since it started), and its commits. An agent's are its landing's;
// a lead phase's are those the checkout made from its start up to its done, less the work that landed
// meanwhile and baton's own records.
func recordEntry(ctx context.Context, s *state.Store, st state.State, pl plan.Plan, id string, usable bool) decide.RecordEntry {
	ps := st.Phases[id]
	e := decide.RecordEntry{Phase: id, Title: pl.Phases[pl.Index(id)].Title, At: ps.DoneAt}
	handoff, _ := os.ReadFile(s.HandoffPath())
	e.Notes = doneNotes(string(handoff), id, ps.StartedAt)
	if !usable {
		return e
	}
	var commits []gitx.Commit
	if l := landingOf(st, id); l != nil {
		e.Landed = l.By != state.LandedByLead
		commits, _ = gitx.Described(ctx, s.Checkout, l.Commits)
	} else if ps.StartHead != "" {
		others := st.RecordCommits()
		for _, l := range st.Landed {
			others = append(others, l.Commits...)
		}
		all, _ := gitx.Log(ctx, s.Checkout, ps.StartHead, "HEAD")
		for _, cm := range all {
			if !slices.Contains(others, cm.Hash) && (ps.DoneAt.IsZero() || !cm.When.After(ps.DoneAt)) {
				commits = append(commits, cm)
			}
		}
	}
	for _, cm := range commits {
		e.Commits = append(e.Commits, cm.Short()+" "+cm.Subject)
	}
	return e
}

// landingOf is the latest landing of phase id's work, or nil.
func landingOf(st state.State, id string) *state.Landing {
	for i := len(st.Landed) - 1; i >= 0; i-- {
		if st.Landed[i].Phase == id {
			return &st.Landed[i]
		}
	}
	return nil
}

// doneNotes picks, from the run's handoff (a "## <phase> — <time>" heading for each notes left), the
// latest notes phase left as it was done: under its own id (a checkpoint's are "<phase> (checkpoint)"),
// since it started.
func doneNotes(handoff, phase string, started time.Time) string {
	from := started.Truncate(time.Minute)
	notes := ""
	for _, sec := range strings.Split("\n"+strings.TrimSpace(handoff), "\n## ")[1:] {
		head, body, _ := strings.Cut(sec, "\n")
		id, at, ok := strings.Cut(head, " — ")
		if !ok || id != phase {
			continue
		}
		if !started.IsZero() {
			if t, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(at), started.Location()); err != nil || t.Before(from) {
				continue
			}
		}
		notes = strings.TrimSpace(body)
	}
	return notes
}

// recorded records phase id's record written to path, in commit, and says so.
func (c *Coordinator) recorded(now time.Time, s *state.Store, id, path, commit string) {
	c.rec.said = ""
	s.Update(func(st *state.State) error {
		if !st.HasRecord(id) {
			st.Records = append(st.Records, state.RecordNote{Phase: id, Path: path, Commit: commit, At: now})
		}
		st.Run.Announce = append(st.Run.Announce, decide.RecordSay(id, path, commit))
		return nil
	})
	s.Event("record_written", map[string]any{"phase": id, "commit": commit, "path": path})
}

// recordHeld says, once a hold, that phase id's record waits, and why; the step looks again a while later.
func (c *Coordinator) recordHeld(now time.Time, s *state.Store, id, path, kind, why string) {
	c.rec.retry.Store(now.Add(recordHeldRetry).UnixNano())
	if key := id + ":" + kind; c.rec.said != key {
		c.rec.said = key
		s.Event("record_held", map[string]any{"phase": id, "path": path, "why": kind})
		c.announce(s, decide.RecordHeld(id, why))
	}
}

// recordFailed says, once a failure, that phase id's record could not be written to path; the step tries
// again a while later, or shortly after a locked index.
func (c *Coordinator) recordFailed(now time.Time, s *state.Store, id, path string, err error) {
	retry := recordFailedRetry
	if errors.Is(err, gitx.ErrLocked) {
		retry = lockedRetry
	}
	c.rec.retry.Store(now.Add(retry).UnixNano())
	if key := id + ":" + err.Error(); c.rec.said != key {
		c.rec.said = key
		c.Lead.logf("loop: %s: record not written: %v", id, err)
		s.Event("record_failed", map[string]any{"phase": id, "path": path, "error": err.Error()})
		c.announce(s, decide.RecordFailed(id, path, err.Error()))
	}
}
