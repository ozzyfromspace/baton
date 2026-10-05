package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func threePhases() plan.Plan {
	return plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"}, {ID: "P2", Title: "c"}}}
}

func TestPhaseLifecycle(t *testing.T) {
	p := threePhases()
	st := Attach(p, t0, "abc")
	if st.Mode != ModeRunning || st.Current != "P0" || st.Phases["P0"].Status != PhaseActive || st.Phases["P0"].StartHead != "abc" {
		t.Fatalf("after attach: %+v", st)
	}
	if _, err := Done(&st, p, "P1", t0, false); err == nil || !strings.Contains(err.Error(), "not the current phase") {
		t.Fatalf("done on a non-current phase: %v", err)
	}
	if _, err := Done(&st, p, "P9", t0, false); err == nil || !strings.Contains(err.Error(), "unknown phase") {
		t.Fatalf("done on an unknown phase: %v", err)
	}
	next, err := Done(&st, p, "P0", t0.Add(time.Hour), false)
	if err != nil || next != "P1" || !st.BoundaryOwed || st.Current != "P1" || st.Phases["P1"].Status != PhasePending {
		t.Fatalf("done P0: next %q err %v state %+v", next, err, st)
	}
	if _, err := Done(&st, p, "P0", t0, false); err == nil || !strings.Contains(err.Error(), "already done") {
		t.Fatalf("done twice: %v", err)
	}
	if _, err := Done(&st, p, "P1", t0, false); err == nil || !strings.Contains(err.Error(), "has not started yet") {
		t.Fatalf("finishing the next phase before the boundary compaction: %v", err)
	}
	Start(&st, t0.Add(2*time.Hour), "def")
	if st.BoundaryOwed || st.Phases["P1"].Status != PhaseActive || st.Phases["P1"].StartHead != "def" {
		t.Fatalf("after start: %+v", st)
	}
	// --force finishes a phase out of order.
	if _, err := Done(&st, p, "P2", t0, true); err != nil {
		t.Fatal(err)
	}
	next, err = Done(&st, p, "P1", t0, false)
	if err != nil || next != "" || st.Mode != ModeComplete || st.Current != "" || st.BoundaryOwed {
		t.Fatalf("final done: next %q err %v state %+v", next, err, st)
	}
}

func TestBlockWaitCheckpointPauseResume(t *testing.T) {
	idle := New()
	for name, err := range map[string]error{
		"blocked":    SetBlocked(&idle, "x", t0),
		"waiting":    SetWaiting(&idle, "x", time.Minute, t0),
		"checkpoint": SetCheckpoint(&idle),
		"pause":      Pause(&idle),
	} {
		if err == nil {
			t.Errorf("%s accepted with no plan attached", name)
		}
	}
	st := Attach(threePhases(), t0, "")
	if err := SetWaiting(&st, "build", 20*time.Minute, t0); err != nil || !st.Waiting.Until.Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("waiting: %v %+v", err, st.Waiting)
	}
	if err := SetBlocked(&st, "", t0); err == nil {
		t.Error("blocked without a reason")
	}
	if err := SetBlocked(&st, "need a decision", t0); err != nil || st.Waiting != nil || st.Blocked.Reason != "need a decision" {
		t.Fatalf("blocked replaces waiting: %v %+v", err, st)
	}
	if err := Resume(&st); err != nil || st.Blocked != nil || st.Mode != ModeRunning {
		t.Fatalf("resume clears a block: %v %+v", err, st)
	}
	if err := SetCheckpoint(&st); err != nil || !st.CheckpointOwed {
		t.Fatal("checkpoint")
	}
	if err := Pause(&st); err != nil || st.Mode != ModePaused {
		t.Fatal("pause")
	}
	if err := SetCheckpoint(&st); err == nil {
		t.Error("checkpoint while paused")
	}
	if err := Resume(&st); err != nil || st.Mode != ModeRunning {
		t.Fatal("resume")
	}
	if err := Resume(&st); err == nil {
		t.Error("resume with nothing to resume")
	}
}

func TestUpdateSerializesConcurrentWriters(t *testing.T) {
	s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Update(func(st *State) error {
				st.Phases[fmt.Sprint(i)] = &PhaseState{Status: PhasePending}
				return s.Event("test", map[string]any{"i": i}) // events are lock-free, so this must not deadlock
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	st, err := s.Load()
	if err != nil || len(st.Phases) != 25 {
		t.Fatalf("lost updates: %d phases, err %v", len(st.Phases), err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if n := strings.Count(string(b), "\n"); n != 25 {
		t.Fatalf("events: %d lines", n)
	}
}

func TestFailedUpdateWritesNothing(t *testing.T) {
	s, _ := Open(t.TempDir(), "", time.Now)
	s.Update(func(st *State) error { st.Mode = ModeRunning; return nil })
	_, err := s.Update(func(st *State) error { st.Mode = ModePaused; return fmt.Errorf("nope") })
	st, _ := s.Load()
	if err == nil || st.Mode != ModeRunning {
		t.Fatalf("err %v mode %s", err, st.Mode)
	}
}

func TestLocate(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	os.MkdirAll(deep, 0o755)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	if got := Locate(deep, env(map[string]string{"BATON_DIR": "/x/.baton"})); got != "/x/.baton" {
		t.Errorf("BATON_DIR: %s", got)
	}
	if got := Locate(deep, env(nil)); got != filepath.Join(deep, ".baton") {
		t.Errorf("no repo: %s", got)
	}
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	if got := Locate(deep, env(nil)); got != filepath.Join(root, ".baton") {
		t.Errorf("repo root: %s", got)
	}
	os.Mkdir(filepath.Join(root, "a", ".baton"), 0o755)
	if got := Locate(deep, env(nil)); got != filepath.Join(root, "a", ".baton") {
		t.Errorf("existing .baton wins: %s", got)
	}

	// A worktree nested inside the main checkout has its own state, even though the main checkout's
	// .baton is above it.
	os.Mkdir(filepath.Join(root, ".baton"), 0o755)
	wt := filepath.Join(root, ".claude", "worktrees", "w")
	os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(root, ".git", "worktrees", "w")+"\n"), 0o644)
	if got := Locate(filepath.Join(wt, "sub"), env(nil)); got != filepath.Join(wt, ".baton") {
		t.Errorf("nested worktree: %s", got)
	}
	if got := Locate(filepath.Join(root, ".claude"), env(nil)); got != filepath.Join(root, ".baton") {
		t.Errorf("main checkout: %s", got)
	}
}

// baton's own home (~/.baton) holds binaries and config; it is never a project's state, even when the
// search reaches it (outside a repository, or with a dotfiles repository at ~).
func TestLocateSkipsBatonHome(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".baton"), 0o755)
	cwd := filepath.Join(home, "notes", "sub")
	os.MkdirAll(cwd, 0o755)
	env := func(k string) string { return map[string]string{"HOME": home}[k] }
	if got := Locate(cwd, env); got != filepath.Join(cwd, ".baton") {
		t.Errorf("outside a repository: %s", got)
	}
	os.Mkdir(filepath.Join(home, ".git"), 0o755)
	if got := Locate(cwd, env); got != filepath.Join(cwd, ".baton") {
		t.Errorf("dotfiles repository at home: %s", got)
	}

	other := t.TempDir()
	os.Mkdir(filepath.Join(other, ".baton"), 0o755)
	inner := filepath.Join(other, "proj")
	os.Mkdir(inner, 0o755)
	if got := Locate(inner, func(k string) string { return map[string]string{"BATON_HOME": filepath.Join(other, ".baton")}[k] }); got != filepath.Join(inner, ".baton") {
		t.Errorf("BATON_HOME: %s", got)
	}
}

func TestExcludeFromGitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755)
	for i := 0; i < 2; i++ {
		if err := ExcludeFromGit(filepath.Join(root, ".baton")); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if strings.Count(string(b), ".baton/") != 1 {
		t.Fatalf("exclude = %q", b)
	}
}

func TestExcludeFromGitFollowsWorktreeFile(t *testing.T) {
	main := t.TempDir()
	wt := t.TempDir()
	gitdir := filepath.Join(main, ".git", "worktrees", "wt")
	os.MkdirAll(gitdir, 0o755)
	os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../.."), 0o644)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644)
	if err := ExcludeFromGit(filepath.Join(wt, ".baton")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(main, ".git", "info", "exclude")); err != nil || !strings.Contains(string(b), ".baton/") {
		t.Fatalf("exclude in common dir: %q %v", b, err)
	}
}

func TestOwnership(t *testing.T) {
	st := New()
	if err := Claim(&st, "a", 1, t0); err != nil || !st.IsOwner("a", t0) {
		t.Fatalf("claim: %v", err)
	}
	if err := Claim(&st, "b", 2, t0.Add(time.Second)); err == nil {
		t.Fatal("a second live host took over")
	}
	if err := Claim(&st, "a", 1, t0.Add(10*time.Second)); err != nil || !st.Owner.Heartbeat.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("heartbeat: %v", err)
	}
	// A stale heartbeat (the machine slept, the host has not ticked yet) does not end ownership...
	if !st.IsOwner("a", t0.Add(time.Hour)) {
		t.Fatal("the owner's own hooks went dormant on a stale heartbeat")
	}
	// ...it only lets another host take over.
	if err := Claim(&st, "b", 2, t0.Add(10*time.Second+OwnerTTL)); err != nil || !st.IsOwner("b", t0.Add(10*time.Second+OwnerTTL)) || st.IsOwner("a", t0) {
		t.Fatalf("stale owner not replaced: %v", err)
	}
	Release(&st, "a") // not the owner any more: no effect
	if st.Owner == nil {
		t.Fatal("release by a non-owner")
	}
	Release(&st, "b")
	if st.Owner != nil {
		t.Fatal("release")
	}
	re := Reattach(State{Owner: &Owner{Instance: "x"}}, threePhases(), t0, "")
	if re.Owner == nil || re.Owner.Instance != "x" {
		t.Fatal("reattach dropped the owner")
	}
}

func TestEventFieldsCannotOverrideReservedKeys(t *testing.T) {
	s, _ := Open(t.TempDir(), "inst", func() time.Time { return t0 })
	s.Event("brief", map[string]any{"kind": "boundary", "ts": "x", "instance": "y"})
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"brief"`) || !strings.Contains(string(b), `"instance":"inst"`) || strings.Contains(string(b), `"ts":"x"`) {
		t.Fatalf("event: %s", b)
	}
}

func TestWaitsAreBounded(t *testing.T) {
	st := Attach(threePhases(), t0, "")
	if err := SetWaiting(&st, "the deploy", MaxWait, t0); err != nil {
		t.Fatal(err)
	}
	if err := SetWaiting(&st, "the overnight batch", MaxWait+time.Minute, t0); err == nil || !strings.Contains(err.Error(), "baton blocked") {
		t.Fatalf("an unbounded wait: %v", err)
	}
}
