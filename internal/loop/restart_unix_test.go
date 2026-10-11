//go:build !windows

package loop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// fakeClaude builds the host's stand-in for claude (internal/host/testdata/fakeclaude).
func fakeClaude(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeclaude")
	if out, err := exec.Command("go", "build", "-o", bin, "../host/testdata/fakeclaude").CombinedOutput(); err != nil {
		t.Fatalf("building fakeclaude: %v\n%s", err, out)
	}
	return bin
}

// onClaude makes coordinator c start its workers' sessions on sv, recording into out what each one
// started with; it returns them as they start.
func onClaude(c *Coordinator, sv *host.Supervisor, out string) *[]*host.Session {
	var started []*host.Session
	c.Fleet.Launch.Env = append(c.Fleet.Launch.Env, "FAKE_OUT="+out, "FAKE_EXIT=7")
	c.Fleet.Spawn = func(spec host.Spec) (Hidden, error) {
		s, err := sv.Spawn(spec)
		if err != nil {
			return nil, err
		}
		started = append(started, s)
		return s, nil
	}
	return &started
}

// startedWith is what the last claude that recorded into out was started with: its arguments, and its
// working directory.
func startedWith(t *testing.T, out string) (args []string, dir string) {
	t.Helper()
	var rec struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(out, "start.json"))
		if err == nil && json.Unmarshal(b, &rec) == nil {
			return rec.Args, rec.Dir
		}
		if time.Now().After(deadline) {
			t.Fatalf("no claude started: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An upgrade restart takes the run's workers with it. Once every worker is idle, the lead restarts; the
// host ends the workers' sessions (claude's /exit) and suspends them, and the host the newer baton runs
// resumes each one in its worktree: the same conversation, told why it ended.
func TestTheNewerBatonResumesTheWorkersARestartEnded(t *testing.T) {
	fake := fakeClaude(t)
	x := newCrew(t, 1)
	config := host.Config{Claude: fake, TypeDelay: time.Millisecond, EnterDelay: 5 * time.Millisecond, HangupGrace: 5 * time.Second}
	out := t.TempDir()
	started := onClaude(x.c, host.NewSupervisor(config), out)
	x.tick(time.Second)
	if args, _ := startedWith(t, out); !slices.Contains(args, "--session-id") || len(*started) != 1 {
		t.Fatalf("the worker started with %q", args)
	}
	x.started("P1")
	x.tick(time.Second)
	before := x.worker("P1")

	// A newer baton is installed, and the lead is at the compaction of a phase boundary.
	x.c.Lead.Version, x.c.Lead.Newer = "v0.5.0", func() string { return "v0.5.1" }
	x.lead.Update(func(st *state.State) error {
		st.Run.SessionID = "sess-1"
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactQueued, Reason: "boundary", Queued: x.now}
		return nil
	})
	x.tick(time.Second)
	if to, _, ok := x.c.Lead.Restart(); !ok || to != "v0.5.1" {
		t.Fatalf("the lead did not restart with its worker idle")
	}

	// The host ends the worker's session as it ends: neither a crash to resume, nor a worker to retire.
	sess := (*started)[0]
	sess.End()
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the worker's claude did not end on /exit")
	}
	if code := sess.ExitCode(); code != 0 {
		t.Errorf("the worker's claude exited %d", code)
	}
	x.tick(time.Second)
	if w := x.worker("P1"); w.Status != state.WorkerWorking || w.Resumes != 0 || len(*started) != 1 {
		t.Fatalf("its end was taken for a crash: %+v, %d sessions", w, len(*started))
	}
	why := "baton restarted on baton v0.5.1"
	if ended := x.c.Ended(why); len(ended) != 1 || x.worker("P1").Status != state.WorkerSuspended {
		t.Fatalf("not suspended: %+v", x.worker("P1"))
	}
	x.proj.Unbind("inst-P1")
	x.proj.Unbind("inst")

	// The newer baton's host.
	x.c = x.coordinator("next", 1)
	out = t.TempDir()
	started = onClaude(x.c, host.NewSupervisor(config), out)
	t.Cleanup(func() {
		for _, s := range *started {
			s.Quit()
			<-s.Done()
		}
	})
	x.tick(time.Second)
	args, dir := startedWith(t, out)
	want := []string{"--resume", before.Session, "--", decide.WorkerResume("P1", why)}
	if len(args) < len(want) || !slices.Equal(args[len(args)-len(want):], want) {
		t.Errorf("resumed with %q, want it to end %q", args, want)
	}
	if real, _ := filepath.EvalSymlinks(before.Worktree); dir != real {
		t.Errorf("resumed in %s, not its worktree %s", dir, real)
	}
	if w := x.worker("P1"); w.Status != state.WorkerStarting || w.Instance != "next-P1" || w.Resumes != 0 {
		t.Errorf("worker %+v", w)
	}
}
