package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// recordCrew is a crew of at most max agents whose plan names the record file docs/RECORD.md (record ""
// for a plan that names none), with git's identity set, as a user's is.
func recordCrew(t *testing.T, max int, record string) *crew {
	t.Helper()
	x := newCrewOn(t, max, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Record: record, Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "P2", Title: "two", After: []string{}}, {ID: "P3", Title: "three", After: []string{"P1"}}}})
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	return x
}

const recordPath = "docs/RECORD.md"

// leadPhase has the lead do phase id: it starts on the checkout's HEAD, commits files there at, leaves
// notes, and is done at at. The lead goes on with P2. It returns the commit.
func (x *crew) leadPhase(id string, files map[string]string, notes string, at time.Time) string {
	x.t.Helper()
	start := gitx.Head(x.repo)
	x.t.Setenv("GIT_COMMITTER_DATE", fmt.Sprintf("@%d +0000", at.Unix()))
	tip := x.leadCommits(files)
	if notes != "" {
		x.lead.AppendHandoff(id, notes)
	}
	if _, err := x.lead.Update(func(st *state.State) error {
		ps := st.Phases[id]
		ps.Status, ps.StartHead, ps.StartedAt, ps.DoneAt = state.PhaseDone, start, at.Add(-time.Minute), at
		if st.Current == id {
			st.Current = "P2"
			st.Phases["P2"].Status, st.Phases["P2"].StartedAt = state.PhaseActive, at
		}
		return nil
	}); err != nil {
		x.t.Fatal(err)
	}
	return tip
}

// recordEntryOf is the record baton writes of phase id, done (or landed) at at, with notes and commits
// (hashes, each with its subject).
func recordEntryOf(id, title string, at time.Time, landed bool, notes string, commits ...string) string {
	return decide.RecordEntry{Phase: id, Title: title, At: at, Landed: landed, Notes: notes, Commits: commits}.Text()
}

// Each lead phase done is recorded in the record file, one at a time in the order the phases were done,
// each in a commit of its own that holds the record file alone: the lead's own staged and unstaged work
// stays as it was. A phase's record lists its own commits, never another phase's or baton's records. The
// lead is told at its next tool call, and the human at once.
func TestRecordsAreAppendedInOrderAndCommittedAlone(t *testing.T) {
	x := recordCrew(t, 0, recordPath)
	// P1 was done before P0, and both wait to be recorded: the order is the order they were done.
	t0, t1 := x.now.Add(-2*time.Minute), x.now.Add(-time.Minute)
	c1 := x.leadPhase("P1", map[string]string{"one.txt": "1\n"}, "One is in.", t0)
	c0 := x.leadPhase("P0", map[string]string{"zero.txt": "0\n"}, "Zero is in.", t1)
	gittest.Write(t, x.repo, "a.txt", "the lead's, staged\n")
	gittest.Git(t, x.repo, "add", "a.txt")
	gittest.Write(t, x.repo, "notes.txt", "the lead's, untracked\n")
	before := gitx.Head(x.repo)

	x.tick(recordEvery)
	st := x.state()
	if len(st.Records) != 1 || st.Records[0].Phase != "P1" {
		t.Fatalf("records %+v\n%s", st.Records, x.events())
	}
	first := st.Records[0].Commit
	if head := gitx.Head(x.repo); first == "" || head != first || gittest.Git(t, x.repo, "rev-parse", "HEAD^") != before {
		t.Fatalf("P1's record commit %s, HEAD %s: want a new commit on %s", first, head, before)
	}
	want := decide.RecordHeader("Demo") + recordEntryOf("P1", "one", t0, false, "One is in.", c1[:7]+" the lead's")
	if got := gittest.Git(t, x.repo, "show", "HEAD:"+recordPath); got != strings.TrimSpace(want) {
		t.Errorf("the record holds:\n%s\nwant:\n%s", got, want)
	}

	x.tick(recordEvery)
	st = x.state()
	if len(st.Records) != 2 || st.Records[1].Phase != "P0" || st.Records[1].Commit != gitx.Head(x.repo) {
		t.Fatalf("records %+v\n%s", st.Records, x.events())
	}
	// P0's commits are its own: not P1's before it, nor baton's record of P1, made since.
	want += recordEntryOf("P0", "zero", t1, false, "Zero is in.", c0[:7]+" the lead's")
	if got := read(t, x.repo, recordPath); got != want {
		t.Errorf("the record holds:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range []string{first, st.Records[1].Commit} {
		if files := gittest.Git(t, x.repo, "show", "--name-only", "--format=", c); files != recordPath {
			t.Errorf("commit %s holds %q, not the record file alone", c, files)
		}
	}
	if msg := gittest.Git(t, x.repo, "log", "-1", "--format=%s"); msg != decide.RecordCommit("P0", "zero") {
		t.Errorf("message %q", msg)
	}
	if staged := gittest.Git(t, x.repo, "show", ":a.txt"); staged != "the lead's, staged" {
		t.Errorf("the lead's staged a.txt is %q", staged)
	}
	if status := gittest.Git(t, x.repo, "status", "--porcelain"); !strings.Contains(status, "M  a.txt") || !strings.Contains(status, "?? notes.txt") || strings.Contains(status, "RECORD") {
		t.Errorf("status:\n%s", status)
	}
	for _, n := range st.Records {
		if !x.has("record_written", `"phase":"`+n.Phase+`"`, `"commit":"`+n.Commit+`"`, `"path":"`+recordPath+`"`) {
			t.Errorf("no record_written for %s:\n%s", n.Phase, x.events())
		}
		if want := decide.RecordSay(n.Phase, recordPath, n.Commit); !strings.Contains(x.announced(), want) {
			t.Errorf("announced %q, want %q", x.announced(), want)
		}
	}

	x.tick(recordEvery)
	if n := strings.Count(x.events(), `"kind":"record_written"`); n != 2 || len(x.state().Records) != 2 {
		t.Errorf("%d records written:\n%s", n, x.events())
	}
	// The lead's model hears of both at its next tool call, once.
	var told []state.RecordNote
	x.lead.Update(func(st *state.State) error { told = state.TakeRecords(st); return nil })
	if len(told) != 2 || told[0].Phase != "P1" || told[1].Phase != "P0" {
		t.Errorf("told %+v", told)
	}
	x.lead.Update(func(st *state.State) error { told = state.TakeRecords(st); return nil })
	if len(told) != 0 {
		t.Errorf("told again: %+v", told)
	}
}

// An agent's phase is recorded once it lands: its notes, left by its `baton done`, and its landing's
// commits.
func TestALandedPhaseIsRecorded(t *testing.T) {
	x := recordCrew(t, 1, recordPath)
	x.atWork("P1")
	x.sub("P1").AppendHandoff("P1", "One, by its agent.")
	tip := x.finish("P1", map[string]string{"one.txt": "one\n"}, false)
	x.tick(time.Second)
	if gitx.Head(x.repo) != tip {
		t.Fatalf("P1 did not land:\n%s", x.events())
	}
	landed := x.state().Phases["P1"].DoneAt
	x.tick(recordEvery)
	st := x.state()
	if len(st.Records) != 1 || st.Records[0].Phase != "P1" {
		t.Fatalf("records %+v\n%s", st.Records, x.events())
	}
	want := decide.RecordHeader("Demo") + recordEntryOf("P1", "one", landed, true, "One, by its agent.", tip[:7]+" P1: the worker's")
	if got := read(t, x.repo, recordPath); got != want {
		t.Errorf("the record holds:\n%s\nwant:\n%s", got, want)
	}
	if gittest.Git(t, x.repo, "rev-parse", "HEAD^") != tip {
		t.Error("the record is not a commit of its own on the landing")
	}
}

// A tool call of the lead holds the record back, as it holds a landing: the record is written once it
// has ended.
func TestALeadToolCallHoldsTheRecord(t *testing.T) {
	x := recordCrew(t, 0, recordPath)
	x.leadPhase("P0", map[string]string{"zero.txt": "0\n"}, "Zero.", x.now)
	before := gitx.Head(x.repo)
	x.lead.Update(func(st *state.State) error {
		st.Run.ToolStarted(state.ToolCall{ID: "t1", Tool: "Bash", Since: x.now})
		return nil
	})
	x.tick(recordEvery)
	if gitx.Head(x.repo) != before || !strings.HasPrefix(read(t, x.repo, recordPath), "<") || len(x.state().Records) != 0 {
		t.Fatalf("written under the lead's tool call:\n%s", x.events())
	}
	x.lead.Update(func(st *state.State) error { st.Run.ToolsEnded(""); return nil })
	x.tick(recordEvery)
	if st := x.state(); len(st.Records) != 1 || gitx.Head(x.repo) == before {
		t.Fatalf("not written once the call ended: %+v\n%s", st.Records, x.events())
	}
}

// A record file with uncommitted changes is left alone, and the record waits for it, told once; once the
// file is committed, the record is appended to it.
func TestADirtyRecordFileHoldsTheRecord(t *testing.T) {
	x := recordCrew(t, 0, recordPath)
	c0 := x.leadPhase("P0", map[string]string{"zero.txt": "0\n"}, "Zero.", x.now)
	gittest.Write(t, x.repo, recordPath, "# Our record\n")
	for range 3 {
		x.tick(recordHeldRetry)
		if read(t, x.repo, recordPath) != "# Our record\n" || len(x.state().Records) != 0 {
			t.Fatalf("the dirty record file was touched:\n%s", x.events())
		}
	}
	if n := strings.Count(x.events(), `"kind":"record_held"`); n != 1 || !x.has("record_held", `"phase":"P0"`, `"why":"dirty"`) {
		t.Errorf("record_held logged %d times:\n%s", n, x.events())
	}
	if want := decide.RecordHeld("P0", decide.RecordDirty(recordPath)); !strings.Contains(x.announced(), want) {
		t.Errorf("announced %q, want %q", x.announced(), want)
	}
	t.Setenv("GIT_COMMITTER_DATE", fmt.Sprintf("@%d +0000", x.now.Unix())) // after P0 was done: not one of its commits
	gittest.Commit(t, x.repo, "our record", recordPath)
	x.tick(recordHeldRetry)
	want := "# Our record\n\n" + recordEntryOf("P0", "zero", x.state().Phases["P0"].DoneAt, false, "Zero.", c0[:7]+" the lead's")
	if got := read(t, x.repo, recordPath); got != want || len(x.state().Records) != 1 {
		t.Errorf("the record holds:\n%s\nwant:\n%s\n%s", got, want, x.events())
	}
}

// A plan that names no record file, or a paused run, writes no record.
func TestNoRecordWithoutTheLineOrWhilePaused(t *testing.T) {
	for _, c := range []struct {
		name   string
		record string
		paused bool
	}{
		{"no Record: line", "", false},
		{"paused", recordPath, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := recordCrew(t, 0, c.record)
			x.leadPhase("P0", map[string]string{"zero.txt": "0\n"}, "Zero.", x.now)
			if c.paused {
				x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
			}
			head := gitx.Head(x.repo)
			for range 3 {
				x.tick(recordHeldRetry)
			}
			if gitx.Head(x.repo) != head || strings.Contains(x.events(), `"kind":"record_`) || len(x.state().Records) != 0 {
				t.Errorf("a record was written:\n%s", x.events())
			}
			if got := read(t, x.repo, recordPath); !strings.HasPrefix(got, "<") {
				t.Errorf("the record file holds %q", got)
			}
		})
	}
}

// A record whose commit fails is taken back: the record file is as it was, committed, or gone if baton
// made it, and nothing of baton's stays staged. A record the file ends with already (a host that ended
// before the run's state recorded it) is not written again: its commit is the file's latest. With no git,
// the file is written, and nothing committed.
func TestWriteRecord(t *testing.T) {
	ctx := context.Background()
	entry := "## P0 — zero\n\nNo notes.\n\n"
	add := recordAppender("Demo", entry)
	repo := func(t *testing.T, identity bool) string {
		gittest.Isolate(t)
		dir := t.TempDir()
		gittest.Git(t, dir, "init", "-q", "-b", "main")
		gittest.Git(t, dir, "config", "user.useConfigOnly", "true") // and no identity in the environment: git refuses to commit
		gittest.Write(t, dir, "a.txt", "a\n")
		gittest.Commit(t, dir, "first")
		if identity {
			gittest.Git(t, dir, "config", "user.name", "test")
			gittest.Git(t, dir, "config", "user.email", "test@example.invalid")
		}
		return dir
	}
	t.Run("a failed commit, a tracked file", func(t *testing.T) {
		dir := repo(t, false)
		gittest.Write(t, dir, recordPath, "# Ours\n")
		gittest.Commit(t, dir, "ours", recordPath)
		if _, dirty, err := writeRecord(ctx, dir, recordPath, true, add, "record"); err == nil || dirty {
			t.Fatalf("dirty %v, err %v", dirty, err)
		}
		if got := read(t, dir, recordPath); got != "# Ours\n" {
			t.Errorf("the file holds %q", got)
		}
		if status := gittest.Git(t, dir, "status", "--porcelain"); status != "" {
			t.Errorf("status %q", status)
		}
	})
	t.Run("a failed commit, a new file", func(t *testing.T) {
		dir := repo(t, false)
		if _, _, err := writeRecord(ctx, dir, recordPath, true, add, "record"); err == nil {
			t.Fatal("committed")
		}
		if status := gittest.Git(t, dir, "status", "--porcelain", "-uall"); status != "" || !strings.HasPrefix(read(t, dir, recordPath), "<") {
			t.Errorf("status %q, the file %q", status, read(t, dir, recordPath))
		}
	})
	t.Run("written already", func(t *testing.T) {
		dir := repo(t, true)
		gittest.Write(t, dir, recordPath, "# Ours\n\n"+entry)
		gittest.Commit(t, dir, "the record, by a host that ended", recordPath)
		head := gitx.Head(dir)
		commit, dirty, err := writeRecord(ctx, dir, recordPath, true, add, "record")
		if err != nil || dirty || commit != head || gitx.Head(dir) != head {
			t.Errorf("commit %s (HEAD %s, was %s), dirty %v, err %v", commit, gitx.Head(dir), head, dirty, err)
		}
	})
	t.Run("no git", func(t *testing.T) {
		dir := t.TempDir()
		commit, dirty, err := writeRecord(ctx, dir, recordPath, false, add, "record")
		if err != nil || dirty || commit != "" || read(t, dir, recordPath) != decide.RecordHeader("Demo")+entry {
			t.Errorf("commit %q, dirty %v, err %v, the file %q", commit, dirty, err, read(t, dir, recordPath))
		}
	})
}

// A phase's done notes are the latest it left under its own id since it started: not a checkpoint's, not
// another phase's, not an earlier run's.
func TestDoneNotes(t *testing.T) {
	started := time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC)
	handoff := "## P1 — 2026-10-11 11:58\n\nAn earlier run's P1.\n\n## P1 (checkpoint) — 2026-10-11 12:05\n\nHalfway.\n\n" +
		"## P2 — 2026-10-11 12:06\n\nAnother phase's.\n\n## P1 — 2026-10-11 12:00\n\nFirst done.\n\n## P1 — 2026-10-11 12:10\n\nDone again,\nin two lines.\n\n"
	for _, c := range []struct {
		name, phase string
		started     time.Time
		want        string
	}{
		{"the latest since it started", "P1", started, "Done again,\nin two lines."},
		{"another phase", "P2", started, "Another phase's."},
		{"none since it started", "P1", started.Add(time.Hour), ""},
		{"a start not known", "P1", time.Time{}, "Done again,\nin two lines."},
		{"none at all", "P3", started, ""},
	} {
		if got := doneNotes(handoff, c.phase, c.started); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// An entry is set off from what the record file holds by a blank line, and a new file starts with a
// header.
func TestRecordAppended(t *testing.T) {
	entry := "## P0 — zero\n\nNo notes.\n\n"
	for _, c := range []struct {
		name, old string
		existed   bool
		want      string
	}{
		{"a new file", "", false, decide.RecordHeader("Demo") + entry},
		{"an empty file", "\n", true, decide.RecordHeader("Demo") + entry},
		{"one ending in a blank line", "# Ours\n\n", true, "# Ours\n\n" + entry},
		{"one ending in a line", "# Ours\n", true, "# Ours\n\n" + entry},
		{"one ending mid-line", "# Ours", true, "# Ours\n\n" + entry},
	} {
		if got := string(recordAppended([]byte(c.old), c.existed, "Demo", entry)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
