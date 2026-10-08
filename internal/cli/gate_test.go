package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// newRepoSession is newSession in a git repository holding a.txt and the plan, committed, and no identity.
func newRepoSession(t *testing.T) (s *session, planFile, root string) {
	gittest.Isolate(t)
	s, planFile = newSession(t)
	root = filepath.Dir(planFile)
	gittest.Init(t, root)
	gittest.Write(t, root, "a.txt", "a\n")
	gittest.Commit(t, root, "first")
	return s, planFile, root
}

func (s *session) attach(planFile string) {
	s.t.Helper()
	s.must(s.must("", "attach", planFile, "--suggest"), "attach", planFile, "--spec", "-")
}

func (s *session) events() string {
	b, _ := os.ReadFile(filepath.Join(s.store().Dir, "events.jsonl"))
	return string(b)
}

// fails runs a command that must fail and returns what it said.
func (s *session) fails(args ...string) string {
	s.t.Helper()
	code, out, errs := s.run("", args...)
	if code == 0 {
		s.t.Fatalf("baton %v succeeded:\n%s", args, out)
	}
	return errs
}

func TestBlockedAndDoneRefuseWorkThePhaseLeftUncommitted(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	gittest.Write(t, root, "old.txt", "the human's, from before the plan\n")
	s.attach(planFile)
	if events := s.events(); !strings.Contains(events, `"git":true`) {
		t.Errorf("attached does not record git: %s", events)
	}
	gittest.Write(t, root, "a.txt", "changed by P0\n")
	gittest.Write(t, root, "src/new.txt", "made by P0\n")

	for _, args := range [][]string{{"blocked", "need", "a", "key", "--tried", "the vault"}, {"done", "P0"}} {
		why := s.fails(args...)
		for _, want := range []string{"    a.txt\n    src/new.txt\n", "Commit it first, degraded if need be (e.g. --no-gpg-sign", "baton note \"<what you did>\" --undo \"<how to repair it>\"",
			"Never discard, stash, reset or unstage work", "--keep-dirty \"<why"} {
			if !strings.Contains(why, want) {
				t.Errorf("%v: refusal lacks %q:\n%s", args, want, why)
			}
		}
		if strings.Contains(why, "old.txt") {
			t.Errorf("%v: refused for work that was there before the phase:\n%s", args, why)
		}
	}
	if n := strings.Count(s.events(), `"kind":"refused"`); n != 2 {
		t.Errorf("%d refused events: %s", n, s.events())
	}
	if st := s.store(); st != nil {
		if x, _ := st.Load(); x.Blocked != nil || x.Phases["P0"].Status != state.PhaseActive {
			t.Fatalf("a refused command changed the state: %+v", x)
		}
	}

	// A checkpoint only says so.
	if out := s.must("", "checkpoint"); !strings.Contains(out, "2 files changed in P0 are not committed yet") {
		t.Errorf("checkpoint: %s", out)
	}

	gittest.Commit(t, root, "P0", "a.txt", "src/new.txt")
	out := s.must("", "done", "P0")
	if !strings.Contains(out, "1 uncommitted file was already there when P0 started and has not changed; baton leaves it alone") {
		t.Errorf("no note about the work from before: %s", out)
	}
	if !strings.Contains(out, "    old.txt") || strings.Contains(out, "warning") {
		t.Errorf("done: %s", out)
	}

	// The next phase starts with old.txt already there. Changing it makes it this phase's work.
	s.startNext()
	gittest.Write(t, root, "old.txt", "edited in P1\n")
	if why := s.fails("done", "P1"); !strings.Contains(why, "    old.txt") {
		t.Errorf("a changed file from before: %s", why)
	}
	out = s.must("", "done", "P1", "--keep-dirty", "the human's scratch file")
	if !strings.Contains(out, "leaving 1 file uncommitted (--keep-dirty: the human's scratch file)") {
		t.Errorf("keep-dirty: %s", out)
	}
	if events := s.events(); !strings.Contains(events, `"keep_dirty":"the human's scratch file"`) {
		t.Errorf("phase_done does not record --keep-dirty: %s", events)
	}
	// An empty reason is no reason.
	s.startNext()
	gittest.Write(t, root, "r1.txt", "R1\n")
	s.fails("blocked", "stuck", "--tried", "everything", "--keep-dirty", " ")
	s.must("", "blocked", "stuck", "--tried", "everything", "--keep-dirty", "it is the thing I am stuck on")
}

// A phase that started before baton recorded its uncommitted work (a run from an older baton, or a
// repository created mid-run) cannot tell old work from new: it warns rather than refuses.
func TestUnknownStartOnlyWarns(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.attach(planFile)
	st := s.store()
	st.Update(func(x *state.State) error { x.Phases["P0"].StartDirty = nil; return nil })
	gittest.Write(t, root, "new.txt", "x\n")
	out := s.must("", "done", "P0")
	if !strings.Contains(out, "baton has no record of what was uncommitted when P0 started") || !strings.Contains(out, "    new.txt") {
		t.Errorf("done: %s", out)
	}
}

// baton's own files are nobody's work, even when git can see them.
func TestBatonsOwnFilesAreNotWork(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.attach(planFile)
	os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), nil, 0o644) // as if git arrived after baton
	s.must("", "done", "P0")
}

// Without git there is nothing to commit: nothing is refused, and nothing mentions committing.
func TestWithoutGitNothingIsRefusedOrCommitted(t *testing.T) {
	plain := func(t *testing.T) (*session, string, string) {
		gittest.Isolate(t)
		s, planFile := newSession(t)
		return s, planFile, filepath.Dir(planFile)
	}
	hidden := func(t *testing.T) (*session, string, string) {
		s, planFile, root := newRepoSession(t)
		gittest.NoGit(t)
		return s, planFile, root
	}
	for name, setup := range map[string]func(*testing.T) (*session, string, string){"plain folder": plain, "git not installed": hidden} {
		t.Run(name, func(t *testing.T) {
			s, planFile, root := setup(t)
			s.attach(planFile)
			var said strings.Builder
			for _, args := range [][]string{{"checkpoint"}, {"blocked", "need", "a", "key", "--tried", "the vault", "--keep-dirty", "ignored"}, {"run"}, {"done", "P0"}} {
				gittest.Write(t, root, args[0]+".txt", "work\n")
				if args[0] == "run" {
					s.human() // only the human can clear a block
				}
				code, out, errs := s.run("", args...)
				if code != 0 {
					t.Fatalf("%v: exit %d: %s", args, code, errs)
				}
				said.WriteString(out + errs)
			}
			if strings.Contains(strings.ToLower(said.String()), "commit") {
				t.Errorf("mentions committing:\n%s", said.String())
			}
			events := s.events()
			if strings.Contains(events, `"kind":"refused"`) || !strings.Contains(events, `"git":false`) {
				t.Errorf("events: %s", events)
			}
			if b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); strings.Contains(string(b), ".baton") {
				t.Error("wrote to .git/info/exclude without git")
			}
			if _, err := os.Stat(filepath.Join(root, ".git")); name == "plain folder" && err == nil {
				t.Error("created a repository")
			}
		})
	}
}

// baton status lists the snapshots the host took and how to get one back.
func TestStatusListsSnapshots(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.attach(planFile)
	if out := s.must("", "status"); strings.Contains(out, "snapshots") {
		t.Fatalf("no snapshot yet: %s", out)
	}
	gittest.Write(t, root, "a.txt", "work in progress\n")
	saved, err := gitx.Snapshot(root, s.now, "P0", "blocked")
	if err != nil {
		t.Fatal(err)
	}
	out := s.must("", "status")
	for _, want := range []string{saved.Ref + "  (P0 · 1 file)", "git show --stat <ref>", "git restore --source=<ref> --worktree -- ."} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	var js struct {
		Snapshots []gitx.Saved `json:"snapshots"`
	}
	json.Unmarshal([]byte(s.must("", "status", "--json")), &js)
	if len(js.Snapshots) != 1 || js.Snapshots[0].Ref != saved.Ref {
		t.Errorf("status --json: %+v", js)
	}
}

// With another session working in the same checkout, a phase is held only to the files this session's
// edit tools wrote: the rest may be the other session's, and committing them would take its work.
func TestASharedCheckoutHoldsAPhaseToItsOwnEdits(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.attach(planFile)
	p, _ := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
	p.Bind(state.Binding{Session: "sess-2", Instance: "inst-2"}) // the other terminal, live
	gittest.Write(t, root, "mine.txt", "P0's\n")
	gittest.Write(t, root, "theirs.txt", "the other session's\n")
	s.set(func(x *state.State) { x.Touched("mine.txt") })

	why := s.fails("done", "P0")
	if !strings.Contains(why, "    mine.txt") || strings.Contains(why, "theirs.txt") {
		t.Fatalf("refusal:\n%s", why)
	}
	gittest.Commit(t, root, "P0", "mine.txt")
	out := s.must("", "done", "P0")
	if !strings.Contains(out, "another baton session is working in this checkout, and 1 uncommitted file changed during P0") || !strings.Contains(out, "    theirs.txt") {
		t.Fatalf("done:\n%s", out)
	}
}
