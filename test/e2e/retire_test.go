//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A run whose host is gone keeps its workers until something retires them: baton clean, a new run in the
// project, a new plan for the run, or a host of the run with workers off. Each scenario runs dropPlan with
// a worker that commits on P1 and then waits on hold.sh, so the worker is still on P1 when that happens.
// They share baton's home between sessions and commands, since it keeps the workers' worktrees.

// workerTools are what the sessions of dropPlan, and the plans that follow it, run without a prompt.
var workerTools = []string{"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write"}

// abandonAfter is how long a run must have been quiet before baton takes it for abandoned, in these
// scenarios (BATON_ABANDON_AFTER; 2 minutes for real).
const abandonAfter = 3 * time.Second

// ownerTTL is a little longer than a host's heartbeat lasts (state.OwnerTTL): a host that was killed
// keeps its run live until then.
const ownerTTL = 35 * time.Second

// stopHost ends the session the way a human's Ctrl-C at the terminal would, and waits for its host to
// end. Its workers are suspended as it ends.
func stopHost(t *testing.T, s *Session) {
	t.Helper()
	s.stop(30 * time.Second)
	if first(s.OwnEvents(), is("host_stopped")) < 0 {
		t.Fatalf("the host did not stop by itself:\n%s", eventLog(s))
	}
}

// batonIn runs baton from a shell in dir, with baton's home and env, and returns what it printed.
func batonIn(t *testing.T, dir, home string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(batonBin, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "BATON_HOME="+home), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baton %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// commitPlan writes a plan document into dir and commits it, for a session to attach.
func commitPlan(t *testing.T, dir, name, doc string) {
	t.Helper()
	writeFile(t, dir, name, doc)
	Git(t, dir, "add", name)
	Git(t, dir, "commit", "-q", "-m", "docs: "+name)
}

// alive reports whether process pid still runs.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// eventually polls cond until it holds or timeout passes, with no session to watch (Until stops waiting
// once its session has exited).
func eventually(timeout time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(timeout); ; time.Sleep(time.Second) {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// wouldEnd is a dry run's line for ending the worker on P1.
var wouldEnd = regexp.MustCompile(`worker P1 of run \S+: would`)

// phaseStatus is the status of phase in the run's state.
func phaseStatus(t *testing.T, dir, run, phase string) string {
	t.Helper()
	var st struct {
		Phases map[string]struct{ Status string } `json:"phases"`
	}
	if err := json.Unmarshal([]byte(readFile(filepath.Join(dir, ".baton", "runs", run), "state.json")), &st); err != nil {
		t.Fatal(err)
	}
	return st.Phases[phase].Status
}

// The host of a run with a worker is killed. baton clean, run from a shell once the run has been quiet
// long enough, retires the worker: its job (hold.sh) is stopped, its worktree removed, and its branch
// kept with the commit that never landed. A dry run before it changes nothing, and a second clean finds
// nothing left.
func TestCleanEndsTheWorkerOfAKilledHost(t *testing.T) {
	dir, home := listPlanProject(t), t.TempDir()
	Attach(t, dir, dropPlan)
	s := StartAgents(t, dir, []string{"BATON_HOME=" + home}, workerTools...)
	s.AutoApprove()
	worktree, branch, hold := workerHolds(t, s, dir)

	s.cmd.Process.Kill()
	<-s.exited
	quiet := []string{"BATON_ABANDON_AFTER=" + abandonAfter.String()}
	var dry string
	if !eventually(ownerTTL+time.Minute, func() bool {
		dry = batonIn(t, dir, home, quiet, "clean", "--dry-run")
		return wouldEnd.MatchString(dry)
	}) {
		t.Fatalf("baton clean --dry-run never offered to end the worker:\n%s", dry)
	}
	if _, err := os.Stat(worktree); err != nil || !alive(hold) {
		t.Fatalf("the dry run changed things: the worktree %v, hold.sh alive %v", err, alive(hold))
	}

	out := batonIn(t, dir, home, quiet, "clean")
	if !strings.HasSuffix(out, "\nbaton: clean\n") {
		t.Errorf("baton clean:\n%s", out)
	}
	ev := s.OwnEvents()
	ended := first(ev, is("worker_ended", "phase", "P1", "why", "baton clean"))
	if ended < 0 || ev[ended]["by"] != "baton clean" || ev[ended]["branch"] != branch || fmt.Sprint(ev[ended]["unlanded"]) != "1" {
		t.Fatalf("no worker_ended for P1 by baton clean, keeping %s:\n%s", branch, eventLog(s))
	}
	if first(ev, is("cleaned")) < 0 {
		t.Error("the run's events lack cleaned")
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still there: %v", worktree, err)
	}
	if log := Git(t, dir, "log", "--format=%s", branch); !strings.Contains(log, "feat: one") {
		t.Errorf("the branch %s lost its commit: %s", branch, log)
	}
	if !eventually(30*time.Second, func() bool { return !alive(hold) }) {
		t.Errorf("the worker's job, hold.sh (pid %d), still runs", hold)
	}
	if again := batonIn(t, dir, home, quiet, "clean"); !strings.HasSuffix(again, "\nbaton: nothing to clean\n") {
		t.Errorf("a second baton clean:\n%s", again)
	}
	none(t, s, "landed", "controller_panic")
}

// nextPlan is a plan of one phase for a session that comes after another.
const nextPlan = `# Next plan

## P0 — Say bee
After: none

Run echo bee, then report P0 done: baton done P0 --notes 'said bee'
`

// Session A's host ends while its worker waits on P1: the worker is suspended, for A's next host. None
// comes. Session B starts in the same project, a run of its own, and attaches a plan: that retires A's
// worker, which no session will come back to. Its worktree is removed and its branch kept, by its name,
// with its commit; P1 is pending again in A's run, and B is told.
func TestANewRunRetiresTheWorkerAnEarlierRunLeft(t *testing.T) {
	dir, home := listPlanProject(t), t.TempDir()
	Attach(t, dir, dropPlan)
	a := StartAgents(t, dir, []string{"BATON_HOME=" + home}, workerTools...)
	a.AutoApprove()
	worktree, branch, _ := workerHolds(t, a, dir)
	stopHost(t, a)
	if first(a.OwnEvents(), is("worker_ended", "phase", "P1", "suspended", "true")) < 0 {
		t.Fatalf("A's worker was not suspended:\n%s", eventLog(a))
	}

	time.Sleep(abandonAfter + time.Second) // A is quiet long enough before B's plan is attached
	commitPlan(t, dir, "next.md", nextPlan)
	// The prompt goes first: --allowedTools takes every argument after it.
	b := StartEnv(t, dir, []string{"BATON_HOME=" + home, "BATON_ABANDON_AFTER=" + abandonAfter.String()},
		"Run exactly this one command with the Bash tool: baton attach next.md --suggested\nThen do what baton says.",
		"--model", "haiku", "--allowedTools", "Bash(echo:*)")
	b.Trust()
	b.AutoApprove()
	var retired map[string]any
	if !b.Until("B to retire A's worker", 4*time.Minute, func() bool {
		if i := first(b.OwnEvents(), is("workers_retired")); i >= 0 {
			retired = b.OwnEvents()[i]
		}
		return retired != nil
	}) {
		t.Fatalf("B retired nothing:\n%s", eventLog(b))
	}
	run := strings.Split(branch, "/")[1]
	if retired["run"] != run || fmt.Sprint(retired["phases"]) != "[P1]" || fmt.Sprint(retired["kept"]) != fmt.Sprintf("map[%s:1]", branch) {
		t.Errorf("workers_retired: %v", retired)
	}
	ev := a.OwnEvents()
	ended := first(ev, is("worker_ended", "phase", "P1", "why", "a new run started in this project"))
	if ended < 0 || ev[ended]["branch"] != branch || fmt.Sprint(ev[ended]["unlanded"]) != "1" {
		t.Fatalf("A's events lack the retired worker:\n%s", eventLog(a))
	}
	if started := b.OwnEvents()[first(b.OwnEvents(), is("host_started"))]; ev[ended]["by"] != started["instance"] {
		t.Errorf("retired by %v, not B's host %v", ev[ended]["by"], started["instance"])
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still there: %v", worktree, err)
	}
	if log := Git(t, dir, "log", "--format=%s", branch); !strings.Contains(log, "feat: one") {
		t.Errorf("the branch %s lost its commit: %s", branch, log)
	}
	if got := phaseStatus(t, dir, run, "P1"); got != "pending" {
		t.Errorf("P1 in A's run is %s", got)
	}
	none(t, b, "landed", "controller_panic")
}

// replacePlan reuses dropPlan's phase ids: its P1 is other work.
const replacePlan = `# Replacing plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write uno
After: none

Write the single word uno into a new file named uno.txt. Commit it: git add uno.txt, then git commit -m 'feat: uno'. Then report P1 done: baton done P1 --notes 'wrote uno'
`

// The lead attaches a new plan over the running one while its worker waits on P1. The new plan has a P1
// too, but other work. The old worker is retired as the plan was replaced: its branch, with its commit,
// is kept under kept/, so the new plan's P1 worker starts on a fresh branch from the run's branch, and
// the old commit never lands.
func TestAReplacedPlansWorkerIsRetired(t *testing.T) {
	dir, home := listPlanProject(t), t.TempDir()
	Attach(t, dir, dropPlan)
	s := StartAgents(t, dir, []string{"BATON_HOME=" + home}, workerTools...)
	s.AutoApprove()
	_, branch, hold := workerHolds(t, s, dir)
	commitPlan(t, dir, "replace.md", replacePlan)
	s.Type("Run exactly this one command with the Bash tool: baton attach replace.md --suggested --replace\nThen do what baton says.")

	s.Until("the new plan to complete", 8*time.Minute, func() bool {
		return first(s.Events(), is("plan_complete")) >= 0 && first(s.Events(), is("landed", "phase", "P1")) >= 0
	})
	ev := s.Events()
	ended := first(ev, is("worker_ended", "phase", "P1", "why", "the run's plan was replaced"))
	if ended < 0 {
		t.Fatalf("the old worker was not retired as the plan was replaced:\n%s", eventLog(s))
	}
	kept := fmt.Sprint(ev[ended]["branch"])
	if !strings.HasPrefix(kept, strings.TrimSuffix(branch, "P1")+"kept/P1-") || fmt.Sprint(ev[ended]["unlanded"]) != "1" {
		t.Errorf("the old worker's branch: %v", ev[ended])
	}
	if log := Git(t, dir, "log", "--format=%s", kept); !strings.Contains(log, "feat: one") {
		t.Errorf("the kept branch %s lost its commit: %s", kept, log)
	}
	spawns := filter(ev, is("worker_spawned", "phase", "P1"))
	if len(spawns) != 2 || spawns[1]["branch"] != branch || slices.IndexFunc(ev, func(e map[string]any) bool { return e["kind"] == "worker_spawned" && e["command"] == spawns[1]["command"] }) < ended {
		t.Errorf("the new plan's P1 agent did not start after the old one was retired: %v", spawns)
	}
	if landed := filter(ev, is("landed", "phase", "P1")); len(landed) != 1 || first(ev, is("landed", "phase", "P1")) < ended {
		t.Errorf("P1 landed %d times, or before the old worker was retired", len(landed))
	}
	log := Git(t, dir, "log", "--format=%s")
	if !strings.Contains(log, "feat: uno") || strings.Contains(log, "feat: one") {
		t.Errorf("the run's branch: %s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "one.txt")); !os.IsNotExist(err) {
		t.Errorf("one.txt is in the checkout: %v", err)
	}
	if !s.Until("hold.sh to stop", 30*time.Second, func() bool { return !alive(hold) }) {
		t.Errorf("the old worker's job, hold.sh (pid %d), still runs", hold)
	}
	none(t, s, "controller_panic", "land_returned")
}

// The host ends while the worker waits on P1, and the session is continued with workers off. The new host
// runs no worker, so it retires the one on record: its phase goes back to the lead, which does P1 itself,
// and the plan completes.
func TestWithWorkersOffTheLeadTakesBackAWorkersPhase(t *testing.T) {
	dir, home := listPlanProject(t), t.TempDir()
	Attach(t, dir, dropPlan)
	s := StartAgents(t, dir, []string{"BATON_HOME=" + home}, workerTools...)
	s.AutoApprove()
	worktree, branch, _ := workerHolds(t, s, dir)
	stopHost(t, s)
	if first(s.OwnEvents(), is("worker_ended", "phase", "P1", "suspended", "true")) < 0 {
		t.Fatalf("the worker was not suspended:\n%s", eventLog(s))
	}

	release(t, dir) // the lead's own wait on P1 ends at once
	// As a human would: baton --continue, with nothing typed. baton gives the lead P1.
	again := StartEnv(t, dir, []string{"BATON_HOME=" + home, "BATON_WORKERS=0"}, append([]string{"--continue", "--model", "haiku"}, workerTools...)...)
	again.Trust()
	again.AutoApprove()
	again.Until("the plan to complete", 8*time.Minute, func() bool { return first(again.OwnEvents(), is("plan_complete")) >= 0 })
	ev := again.OwnEvents()
	restarted := first(ev, func(e map[string]any) bool {
		return e["kind"] == "host_started" && fmt.Sprint(e["pid"]) == fmt.Sprint(again.cmd.Process.Pid)
	})
	if restarted < 0 {
		t.Fatalf("the session was not continued in the same run:\n%s", eventLog(again))
	}
	retired, _ := eventAfter(ev, restarted, "worker_ended", func(e map[string]any) bool { return e["why"] == "workers are off in this session" })
	if retired == nil || retired["branch"] != branch || fmt.Sprint(retired["unlanded"]) != "1" {
		t.Fatalf("the worker was not retired by the host with workers off:\n%s", eventLog(again))
	}
	if !before(ev[restarted:], is("worker_ended", "why", "workers are off in this session"), is("phase_started", "phase", "P1")) ||
		!before(ev[restarted:], is("phase_started", "phase", "P1"), is("phase_done", "phase", "P1")) || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("the lead did not take P1 back and finish the plan:\n%s", eventLog(again))
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still there: %v", worktree, err)
	}
	if log := Git(t, dir, "log", "--format=%s"); strings.Count(log, "feat: one") != 1 {
		t.Errorf("the run's branch: %s", log)
	}
	if first(ev[restarted:], is("worker_spawned")) >= 0 {
		t.Error("a worker started with workers off")
	}
	none(t, again, "controller_panic")
}
