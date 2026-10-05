package loop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// newRepoRig is a rig whose project is a git repository holding a.txt (committed), with P0 under way
// and nothing owed.
func newRepoRig(t *testing.T) (*rig, string) {
	gittest.Isolate(t)
	root := t.TempDir()
	gittest.Init(t, root)
	gittest.Write(t, root, "a.txt", "a\n")
	gittest.Commit(t, root, "first")
	return newRigAt(t, filepath.Join(root, ".baton")), root
}

func newRigAt(t *testing.T, dir string) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), in: &typed{}, push: &pushes{}}
	store, _ := state.Open(dir, "inst", func() time.Time { return r.now })
	state.ExcludeFromGit(dir)
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"}}}
	store.Update(func(st *state.State) error {
		*st = state.Attach(pl, r.now, state.Origin{})
		return state.Claim(st, "inst", 1, r.now)
	})
	r.loop = &Loop{Store: store, Notify: r.push, Project: "demo", Timing: DefaultTiming}
	r.view = host.View{Owner: true, LastOutput: r.now.Add(-time.Hour), LastHumanKey: r.now.Add(-time.Hour)}
	return r
}

// snapshots returns the snapshot events so far, once any snapshot still running has finished.
func (r *rig) snapshots() []map[string]any {
	r.loop.saving.Wait()
	b, _ := os.ReadFile(filepath.Join(r.loop.Store.Dir, "events.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && strings.HasPrefix(ev["kind"].(string), "snapshot") {
			out = append(out, ev)
		}
	}
	return out
}

// The run halts for the human: the work is saved once at the start of the halt, recorded on the
// escalation, and saved again only at the next halt.
func TestTheWorkIsSavedOncePerHalt(t *testing.T) {
	r, root := newRepoRig(t)
	gittest.Write(t, root, "a.txt", "work in progress\n")
	r.tick(time.Second)
	if s := r.snapshots(); len(s) != 0 {
		t.Fatalf("saved while running: %v", s)
	}

	r.set(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "need a key", Since: r.now}
	})
	r.tick(time.Second)
	s := r.snapshots()
	if len(s) != 1 || s[0]["kind"] != "snapshot" || s[0]["files"] != 1.0 || s[0]["why"] != "the human was asked: blocked" {
		t.Fatalf("snapshots %v", s)
	}
	ref := s[0]["ref"].(string)
	if !strings.HasPrefix(ref, "refs/baton/snapshots/P0-") || gittest.Git(t, root, "show", ref+":a.txt") != "work in progress" {
		t.Fatalf("ref %q", ref)
	}
	if e := r.state().Run.Escalation; e == nil || e.Snapshot != ref {
		t.Fatalf("the escalation does not name the snapshot: %+v", e)
	}
	for range 5 {
		r.tick(time.Second)
	}
	if s := r.snapshots(); len(s) != 1 {
		t.Fatalf("saved again during the same halt: %v", s)
	}

	// The human answers and the run moves on; later it is paused: a new halt.
	r.set(func(st *state.State) { st.Run.Escalation = nil })
	r.tick(time.Second)
	gittest.Write(t, root, "b.txt", "more\n")
	r.set(func(st *state.State) { st.Mode = state.ModePaused })
	r.tick(time.Second)
	if s := r.snapshots(); len(s) != 2 || s[1]["ref"] == ref || s[1]["files"] != 2.0 || s[1]["why"] != "baton was paused" {
		t.Fatalf("pause: %v", s)
	}

	// Resumed, then a usage limit: a halt too. Nothing changed since the pause, so nothing new is saved.
	r.set(func(st *state.State) { st.Mode = state.ModeRunning })
	r.tick(time.Second)
	r.set(func(st *state.State) { st.Run.LastError = &state.StopError{Error: "rate_limit", At: r.now} })
	r.tick(time.Second)
	if s := r.snapshots(); len(s) != 3 || s[2]["why"] != "a usage limit was reached" || s[2]["unchanged"] != true || s[2]["ref"] != s[1]["ref"] {
		t.Fatalf("rate limit: %v", s)
	}
}

// A dialog is a halt once it has waited long enough to be pushed to the human.
func TestADialogHaltsOnceItHasWaited(t *testing.T) {
	r, root := newRepoRig(t)
	gittest.Write(t, root, "a.txt", "work in progress\n")
	r.tick(time.Second)
	r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Since: r.now}) })
	for range 17 {
		r.tick(10 * time.Second) // 2m50s
	}
	if s := r.snapshots(); len(s) != 0 {
		t.Fatalf("saved before the dialog was pushed: %v", s)
	}
	r.tick(10 * time.Second)
	if s := r.snapshots(); len(s) != 1 || s[0]["why"] != "a dialog waited on the human (Bash)" {
		t.Fatalf("snapshots %v", s)
	}
}

// The session ending saves the work once more, for the session that owned the project.
func TestTheSessionEndingSavesTheWork(t *testing.T) {
	r, root := newRepoRig(t)
	gittest.Write(t, root, "a.txt", "work in progress\n")
	r.tick(time.Second)
	r.loop.Ended()
	if s := r.snapshots(); len(s) != 1 || s[0]["why"] != "the session ended" {
		t.Fatalf("snapshots %v", s)
	}

	other := &Loop{Store: r.loop.Store, Timing: DefaultTiming} // never owned the project
	gittest.Write(t, root, "a.txt", "more\n")
	other.Ended()
	if s := r.snapshots(); len(s) != 1 {
		t.Fatalf("a session that did not own the project saved: %v", s)
	}
	r.set(func(st *state.State) { st.Mode = state.ModeComplete })
	r.loop.Ended()
	if s := r.snapshots(); len(s) != 1 {
		t.Fatalf("saved after the plan was complete: %v", s)
	}
}

func TestAFailedSnapshotIsReported(t *testing.T) {
	r, root := newRepoRig(t)
	gittest.Write(t, root, "a.txt", "work in progress\n")
	r.loop.Snapshot = func(string, time.Time, string, string) (gitx.Saved, error) {
		return gitx.Saved{}, errors.New("disk full")
	}
	r.set(func(st *state.State) { st.Mode = state.ModePaused })
	r.tick(time.Second)
	if s := r.snapshots(); len(s) != 1 || s[0]["kind"] != "snapshot_failed" || s[0]["error"] != "disk full" {
		t.Fatalf("snapshots %v", s)
	}
}

// Without git a halt saves nothing and nothing fails.
func TestNoSnapshotWithoutGit(t *testing.T) {
	halt := func(r *rig) {
		r.set(func(st *state.State) { st.Run.Escalation = &state.Escalation{Kind: "blocked", Since: r.now} })
		r.tick(time.Second)
		r.loop.Ended()
	}
	gittest.Isolate(t)
	plain := t.TempDir()
	gittest.Write(t, plain, "a.txt", "work\n")
	r := newRigAt(t, filepath.Join(plain, ".baton"))
	halt(r)
	if s := r.snapshots(); len(s) != 0 {
		t.Errorf("plain folder: %v", s)
	}

	r, root := newRepoRig(t)
	gittest.Write(t, root, "a.txt", "work\n")
	gittest.NoGit(t)
	halt(r)
	if s := r.snapshots(); len(s) != 0 {
		t.Errorf("git not installed: %v", s)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "refs", "baton")); err == nil {
		t.Error("a ref was written")
	}
}
