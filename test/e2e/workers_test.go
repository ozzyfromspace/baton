//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// workerPlan has two phases that can go ahead at once: the lead takes P0, and a worker P1. P2 needs both.
const workerPlan = `# Worker plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// WorkerEvents reads the event log of the run's worker on phase (.baton/runs/*/workers/<phase>/events.jsonl).
func (s *Session) WorkerEvents(phase string) []map[string]any {
	logs, _ := filepath.Glob(filepath.Join(s.Dir, ".baton", "runs", "*", "workers", phase, "events.jsonl"))
	var out []map[string]any
	for _, path := range logs {
		out = append(out, readEvents(path)...)
	}
	return out
}

// logEvents logs every event, for a failure's diagnosis.
func logEvents(t *testing.T, of string, events []map[string]any) {
	t.Logf("events of %s:", of)
	for _, e := range events {
		b, _ := json.Marshal(e)
		t.Logf("  %s", b)
	}
}

// StartWorkers starts the lead on the attached plan, with one worker beside it. If the test fails, it
// ends the session, then logs the run's events, and each worker's events and the last of its screen
// (screen.log, kept as its session ended).
func StartWorkers(t *testing.T, dir string, env []string, args ...string) *Session {
	t.Helper()
	s := StartEnv(t, dir, append([]string{"BATON_WORKERS=1"}, env...),
		append([]string{"Start the attached plan, and follow baton's instructions.", "--model", "haiku"}, args...)...)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		s.stop(20 * time.Second) // the workers end with the lead, and keep their screens
		logEvents(t, "the run", s.Events())
		subs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "workers", "*"))
		for _, sub := range subs {
			logEvents(t, "the worker on "+filepath.Base(sub), readEvents(filepath.Join(sub, "events.jsonl")))
			if b, err := os.ReadFile(filepath.Join(sub, "screen.log")); err == nil {
				t.Logf("the screen of the worker on %s, last:\n%s", filepath.Base(sub), tail(ansi.ReplaceAllString(string(b), ""), 3000))
			}
		}
	})
	s.Trust()
	return s
}

// listPlanProject makes a project with a file both sessions change, list.txt (alpha to epsilon, a word a
// line), and hold.sh, which waits until the test releases it (release).
func listPlanProject(t *testing.T) string {
	t.Helper()
	dir := NewProject(t)
	files := map[string]string{
		"list.txt": "alpha\nbeta\ngamma\ndelta\nepsilon\n",
		"hold.sh":  holdScript,
	}
	for name, text := range files {
		writeFile(t, dir, name, text)
		Git(t, dir, "add", name)
	}
	Git(t, dir, "commit", "-q", "-m", "chore: the list, and the hold")
	return dir
}

// holdScript waits until the test releases it (release), from a worktree too.
const holdScript = `#!/bin/sh
# Waits until the test releases it (the file go in the repository's git directory), from a worktree too.
common=$(git rev-parse --git-common-dir)
echo $$ >"$common/hold.pid"
echo "hold: waiting"
i=0
while [ ! -e "$common/go" ] && [ "$i" -lt 900 ]; do
	sleep 1
	i=$((i + 1))
done
echo "hold: released"
`

func writeFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

// release lets every hold.sh of the project end.
func release(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, ".git"), "go", "") }

// before reports that the first event cond matches comes before the first one then matches, both there.
func before(events []map[string]any, cond, then func(map[string]any) bool) bool {
	i, j := first(events, cond), first(events, then)
	return i >= 0 && j > i
}

// A worker takes up P1 beside the lead, which works on P0: in a worktree of its own, from its own brief,
// with the lead's permission rules. Once it reports P1 done, its commit lands on the run's branch under
// the lead, the worker ends, and P2, which needs both, starts only then. The lead hears what landed.
func TestAWorkerWorksBesideTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, workerPlan)
	s := StartWorkers(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	spawned := s.WaitEvent("worker_spawned", 2*time.Minute)
	if spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no worker on P1: %v", spawned)
	}
	worktree, _ := spawned["worktree"].(string)
	s.Until("the plan to complete", 8*time.Minute, func() bool {
		return first(s.Events(), is("plan_complete")) >= 0 || first(s.Events(), is("phase_done", "phase", "P2")) >= 0
	})
	if first(s.Events(), is("worker_started", "phase", "P1")) < 0 {
		t.Error("the worker never started")
	}
	w := s.WorkerEvents("P1")
	for i, want := range []func(map[string]any) bool{is("phase_started", "phase", "P1"), is("brief", "type", "worker"), is("phase_done", "phase", "P1"), is("worker_finished", "phase", "P1")} {
		if first(w, want) < 0 {
			t.Fatalf("the worker's events lack step %d", i)
		}
	}
	ev := s.Events()
	landed := first(ev, is("landed", "phase", "P1"))
	if first(ev, is("land_queued", "phase", "P1")) < 0 || landed < 0 {
		t.Fatal("P1's work did not land")
	}
	if p2 := first(ev, is("phase_started", "phase", "P2")); p2 < landed || first(ev, is("phase_done", "phase", "P0")) > p2 {
		t.Errorf("P2 started (event %d) before P0 was done or P1 landed (event %d)", p2, landed)
	}
	if log := Git(t, dir, "log", "--format=%s"); !strings.Contains(log, "feat: one") {
		t.Errorf("the run's branch lacks the worker's commit: %s", log)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "one.txt")); err != nil || strings.TrimSpace(string(got)) != "one" {
		t.Errorf("one.txt in the checkout: %q, %v", got, err)
	}
	if first(ev, is("worker_ended", "phase", "P1", "why", "its work landed")) < 0 {
		t.Error("the landed worker did not end")
	}
	if _, err := os.Stat(worktree); err == nil {
		t.Errorf("the landed worker's worktree %s is still there", worktree)
	}
	if first(ev, is("landed_told")) < 0 {
		t.Error("the lead was never told what landed")
	}
	none(t, s, "workers_off", "worker_resumed", "controller_panic", "land_returned", "land_deferred")
}

// overlapPlan: the lead's P0 holds a change to list.txt uncommitted until the test releases it, while
// the worker's P1 changes list.txt too, on another line.
const overlapPlan = `# Overlap plan

## P0 — Commit the first line
After: none

list.txt already has a change of yours that is not committed yet: its first line is ALPHA now. Leave it uncommitted until step 3.

1. Hand baton this wait, exactly: baton await 'sh hold.sh' --notes 'the change to list.txt is not committed yet; commit it once the wait ends'
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
3. When baton gives you the result, commit list.txt: git add list.txt, then git commit -m 'feat: alpha'. Then report P0 done: baton done P0 --notes 'alpha committed'

## P1 — Change the last line
After: none

Change the last line of list.txt from epsilon to EPSILON, and leave every other line as it is. Commit it: git add list.txt, then git commit -m 'feat: epsilon'. Then report P1 done: baton done P1 --notes 'epsilon'
`

// The worker's work changes list.txt, which the lead has changed and not committed: its landing waits,
// since the fast-forward would refuse it, or worse. Once the lead commits, baton rebases the worker's
// branch onto the lead's commit and lands it, and list.txt keeps both changes.
func TestALandingWaitsForTheLeadsChanges(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, overlapPlan)
	writeFile(t, dir, "list.txt", "ALPHA\nbeta\ngamma\ndelta\nepsilon\n") // the lead's change, not committed
	s := StartWorkers(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Edit", "Write")
	s.AutoApprove()

	deferred := s.WaitEvent("land_deferred", 6*time.Minute)
	if deferred == nil || deferred["phase"] != "P1" || fmt.Sprint(deferred["paths"]) != "[list.txt]" {
		t.Fatalf("P1's landing was not deferred for list.txt: %v", deferred)
	}
	if log := Git(t, dir, "log", "--format=%s"); strings.Contains(log, "feat: epsilon") || strings.Contains(log, "feat: alpha") {
		t.Fatalf("committed before the lead was released: %s", log)
	}
	release(t, dir)
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if !before(ev, is("land_deferred", "phase", "P1"), is("rebased", "phase", "P1")) || !before(ev, is("rebased", "phase", "P1"), is("landed", "phase", "P1")) {
		t.Fatal("P1 did not land, rebased onto the lead's commit, after it waited")
	}
	log := Git(t, dir, "log", "--format=%s")
	if a, e := strings.Index(log, "feat: alpha"), strings.Index(log, "feat: epsilon"); a < 0 || e < 0 || e > a {
		t.Errorf("the run's branch, newest first: %s", log)
	}
	if got := readFile(dir, "list.txt"); got != "ALPHA\nbeta\ngamma\ndelta\nEPSILON\n" {
		t.Errorf("list.txt: %q", got)
	}
	none(t, s, "land_returned", "controller_panic", "worker_waiting", "worker_resumed")
}

// conflictPlan: the worker's P1 changes the third line of list.txt, which the run's branch changes
// meanwhile.
const conflictPlan = `# Conflict plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Change the third line
After: none

1. Change the third line of list.txt from gamma to gamma (worker), and leave every other line as it is.
2. Commit it: git add list.txt, then git commit -m 'feat: the worker gamma'.
3. Report P1 done: baton done P1 --notes 'gamma changed'

If baton then says your work conflicts with the run's branch, rebase as it says. In list.txt, keep both versions of the third line: the one from the run's branch first, then yours on the line after it, and leave the other lines as they are.
`

// The run's branch changes the line the worker changes, after the worker started: its rebase onto the
// run's branch conflicts, so baton hands the phase back to the worker, which rebases and resolves it
// itself and reports it done again. Then it lands, with both changes.
func TestAConflictGoesBackToTheWorker(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, conflictPlan)
	s := StartWorkers(t, dir, nil, "--allowedTools", "Bash(git:*)", "Bash(echo:*)", "Edit", "Write")
	s.AutoApprove()

	if spawned := s.WaitEvent("worker_spawned", 2*time.Minute); spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no worker on P1: %v", spawned)
	}
	// Someone in the checkout changes the same line on the run's branch, once the worker's branch is made.
	writeFile(t, dir, "list.txt", "alpha\nbeta\ngamma (lead)\ndelta\nepsilon\n")
	Git(t, dir, "commit", "-q", "-m", "feat: the lead gamma", "list.txt")

	s.Until("the plan to complete", 10*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	returned := first(ev, is("land_returned", "phase", "P1"))
	if returned < 0 || fmt.Sprint(ev[returned]["paths"]) != "[list.txt]" {
		t.Fatal("the conflict did not go back to the worker")
	}
	if !before(ev, is("land_returned", "phase", "P1"), is("landed", "phase", "P1")) {
		t.Fatal("P1 never landed after its conflict")
	}
	if w := s.WorkerEvents("P1"); len(filter(w, is("phase_done", "phase", "P1"))) < 2 {
		t.Error("the worker did not report P1 done again")
	}
	log := Git(t, dir, "log", "--format=%s")
	if l, w := strings.Index(log, "feat: the lead gamma"), strings.Index(log, "feat: the worker gamma"); l < 0 || w < 0 || w > l {
		t.Errorf("the run's branch, newest first: %s", log)
	}
	got := readFile(dir, "list.txt")
	if l, w := strings.Index(got, "gamma (lead)"), strings.Index(got, "gamma (worker)"); l < 0 || w < l || strings.Contains(got, "<<<<<<<") ||
		!strings.HasPrefix(got, "alpha\nbeta\n") || !strings.HasSuffix(got, "delta\nepsilon\n") {
		t.Errorf("list.txt: %q", got)
	}
	none(t, s, "controller_panic", "worker_waiting", "worker_resumed")
}

// filter is the events cond matches.
func filter(events []map[string]any, cond func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if cond(e) {
			out = append(out, e)
		}
	}
	return out
}

// relayPlan: the worker's P1 proposes what it will do, and does what baton tells it.
const relayPlan = `# Relay plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Greet
After: none

1. Before anything else, run exactly this one command with the Bash tool: baton propose "write the word hello into greeting.txt with the Write tool" --because "the plan does not say which greeting to use" --undo "remove greeting.txt"
2. Then do exactly what baton tells you.
3. Once greeting.txt holds hello, commit it: git add greeting.txt, then git commit -m 'feat: greeting'. Then report P1 done: baton done P1 --notes 'greeted'
`

// A worker's proposal is put to the human in the lead's terminal. Nobody answers, so at its deadline
// baton picks Go ahead in the lead (it types 3 once the question is alone on screen); the answer goes
// back to the worker, which does what it proposed, and its work lands. acceptEdits lets the Write tool
// through without a prompt, so the only dialog is baton's question, which AutoApprove's Enter would
// answer Wait for me.
func TestAWorkersProposalIsAskedInTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, relayPlan)
	s := StartWorkers(t, dir, []string{"BATON_ESCALATION_TIMEOUT=20s"}, "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)")

	waitSequence(t, s, 8*time.Minute,
		is("relay_queued", "phase", "P1", "type", "proposal"),
		is("relay_asked", "phase", "P1"),
		func(e map[string]any) bool { return e["kind"] == "auto_answered" && e["key"] == "3" },
		is("relay_answered", "phase", "P1", "by", "timeout", "answer", "Go ahead"),
		is("relay_applied", "phase", "P1"),
		is("landed", "phase", "P1"),
	)
	if got := strings.TrimSpace(readFile(dir, "greeting.txt")); got != "hello" {
		t.Errorf("greeting.txt in the checkout: %q", got)
	}
	if first(s.WorkerEvents("P1"), is("proposed", "phase", "P1")) < 0 {
		t.Error("the worker's own log lacks its proposal")
	}
	none(t, s, "controller_panic", "worker_waiting", "relay_withdrawn")
}

// viewPlan: the worker's P1 runs a command nobody allowed, so it waits on a permission prompt.
const viewPlan = `# View plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Touch a file
After: none

Run exactly this one command with the Bash tool: touch viewed.txt
Then commit it: git add viewed.txt, then git commit -m 'feat: viewed'. Then report P1 done: baton done P1 --notes 'touched'
`

// The worker stops at a permission prompt baton never answers: the lead hears it waits on the human,
// and baton status names baton view. The human runs baton view P1 in another terminal, which shows the
// worker's screen, repainted whole, and their Enter allows the command in the worker. The worker then
// finishes, its work lands, and the viewer is told the worker ended.
func TestViewShowsAWorkerAndTypesIntoIt(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, viewPlan)
	s := StartWorkers(t, dir, nil, "--permission-mode", "default", "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)")
	s.AutoApprove()

	waiting := s.WaitEvent("worker_waiting", 5*time.Minute)
	if waiting == nil || waiting["phase"] != "P1" || !strings.Contains(fmt.Sprint(waiting["what"]), "permission prompt") {
		t.Fatalf("the lead never heard that P1 waits on a permission prompt: %v", waiting)
	}
	status := exec.Command(batonBin, "status")
	status.Dir = dir
	if out, err := status.CombinedOutput(); err != nil || !strings.Contains(string(out), "baton view P1") {
		t.Errorf("baton status: %v\n%s", err, out)
	}

	v := StartProgram(t, dir, nil, batonBin, "view", "P1")
	if !v.Until("the viewer to attach", 30*time.Second, func() bool { return first(s.WorkerEvents("P1"), is("viewer_attached")) >= 0 }) {
		t.Fatalf("the viewer never attached; its screen: %s", v.Screen())
	}
	if !v.WaitScreen("viewed.txt", 30*time.Second) {
		t.Fatalf("the worker's screen, with its prompt, never reached the viewer: %s", tail(v.Screen(), 2000))
	}
	time.Sleep(time.Second)
	v.Key("\r") // the human allows the command
	if !s.Until("P1 to land", 5*time.Minute, func() bool { return first(s.Events(), is("landed", "phase", "P1")) >= 0 }) {
		t.Fatalf("P1 never landed; the viewer's screen: %s", tail(v.Screen(), 2000))
	}
	if _, err := os.Stat(filepath.Join(dir, "viewed.txt")); err != nil {
		t.Errorf("viewed.txt in the checkout: %v", err)
	}
	select {
	case <-v.exited:
	case <-time.After(30 * time.Second):
		t.Error("the viewer stayed after the worker ended")
	}
	if first(s.WorkerEvents("P1"), is("viewer_detached")) < 0 {
		t.Error("the worker's log lacks the viewer leaving")
	}
	none(t, s, "controller_panic", "worker_resumed")
}

// dropPlan: the worker's P1 commits, then waits on hold.sh, which the test never releases.
const dropPlan = `# Drop plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one, then wait
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand baton this wait, exactly: baton await 'sh hold.sh' --notes 'one.txt committed; report P1 done once the wait ends'
3. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
4. When baton gives you the result, report P1 done: baton done P1 --notes 'wrote one'
`

// workerHolds waits, in a run of dropPlan, until the worker on P1 has committed and waits on hold.sh, and
// the lead has finished P0 and is quiet. It returns the worker's worktree and branch, and hold.sh's pid.
func workerHolds(t *testing.T, s *Session, dir string) (worktree, branch string, hold int) {
	t.Helper()
	spawned := s.WaitEvent("worker_spawned", 2*time.Minute)
	if spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no worker on P1: %v", spawned)
	}
	worktree, branch = fmt.Sprint(spawned["worktree"]), fmt.Sprint(spawned["branch"])
	if !s.Until("the worker's wait to start", 5*time.Minute, func() bool { return first(s.WorkerEvents("P1"), is("job_started")) >= 0 }) {
		t.Fatal("the worker never handed baton its wait")
	}
	if !s.Until("hold.sh to start", 30*time.Second, func() bool {
		hold, _ = strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, ".git"), "hold.pid")))
		return hold > 0
	}) {
		t.Fatal("hold.sh never started")
	}
	if !s.Until("the lead to finish P0", 3*time.Minute, func() bool { return first(s.Events(), is("phase_done", "phase", "P0")) >= 0 }) {
		t.Fatal("the lead never finished P0")
	}
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	return worktree, branch, hold
}

// Dropping the run while its worker works ends the worker: its session and its job stop, and its
// worktree is removed, but its branch stays, since it holds a commit that never landed.
func TestDropEndsTheWorkers(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, dropPlan)
	s := StartWorkers(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	worktree, branch, hold := workerHolds(t, s, dir)
	s.Type("Run exactly this one command with the Bash tool: baton drop\nThen reply DONE.")
	if s.WaitEvent("dropped", 2*time.Minute) == nil {
		t.Fatal("the run was not dropped")
	}
	var ended map[string]any
	s.Until("the worker to end", time.Minute, func() bool {
		i := first(s.Events(), is("worker_ended", "phase", "P1"))
		if i >= 0 {
			ended = s.Events()[i]
		}
		return i >= 0
	})
	// A dropped run's branch is kept under kept/, so no later worker on P1 starts from its commits.
	kept := fmt.Sprint(ended["branch"])
	if ended == nil || ended["why"] != "the run was dropped" || !strings.HasPrefix(kept, strings.TrimSuffix(branch, "P1")+"kept/P1-") || fmt.Sprint(ended["unlanded"]) != "1" {
		t.Fatalf("worker_ended: %v", ended)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still there: %v", worktree, err)
	}
	if log := Git(t, dir, "log", "--format=%s", kept); !strings.Contains(log, "feat: one") {
		t.Errorf("the kept branch %s lost its commit: %s", kept, log)
	}
	if log := Git(t, dir, "log", "--format=%s"); strings.Contains(log, "feat: one") {
		t.Errorf("the dropped worker's commit landed: %s", log)
	}
	if !s.Until("hold.sh to stop", time.Minute, func() bool { return syscall.Kill(hold, 0) != nil }) {
		t.Errorf("the worker's job, hold.sh (pid %d), still runs", hold)
	}
	none(t, s, "landed", "controller_panic", "worker_resumed")
}

// autoPlan is three phases that need nothing from each other, and one that needs them all. The lead's P0
// waits on hold.sh until the test releases it, so the lead takes no other phase meanwhile.
const autoPlan = `# Auto plan

## P0 — Wait
After: none

1. Hand baton this wait, exactly: baton await 'sh hold.sh' --notes 'nothing to commit; report P0 done once the wait ends'
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
3. When baton gives you the result, report P0 done: baton done P0 --notes 'waited'

## P1 — Write one
After: none

Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Write two
After: none

Write the single word two into a new file named two.txt. Commit it: git add two.txt, then git commit -m 'feat: two'. Then report P2 done: baton done P2 --notes 'wrote two'

## P3 — Say three
After: all

Run echo three, then report P3 done: baton done P3 --notes 'said three'
`

// With workers: auto, the plan decides how many work at once: P1 and P2 can go ahead beside the lead's P0,
// so two workers start, a ramp apart, on a machine with room for them. Both land, and P3, which needs
// every phase, starts after. No worker starts for P3: by then nothing else can go ahead.
func TestAutoWorkersFollowThePlan(t *testing.T) {
	const ramp = 20 * time.Second
	dir := listPlanProject(t)
	Attach(t, dir, autoPlan)
	s := StartWorkers(t, dir, []string{"BATON_WORKERS=auto", "BATON_WORKER_RAMP=" + ramp.String(), "BATON_SWITCH_AFTER=10m"},
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	if !s.Until("two workers", 4*time.Minute, func() bool { return len(filter(s.Events(), is("worker_spawned"))) >= 2 }) {
		t.Fatalf("fewer than two workers started:\n%s", eventLog(s))
	}
	release(t, dir)
	s.Until("the plan to complete", 8*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	spawns := filter(ev, is("worker_spawned"))
	if len(spawns) != 2 || spawns[0]["phase"] != "P1" || spawns[1]["phase"] != "P2" {
		t.Fatalf("workers started: %v", spawns)
	}
	if apart := eventTime(spawns[1]).Sub(eventTime(spawns[0])); apart < ramp-2*time.Second {
		t.Errorf("the second worker started %s after the first, within the ramp (%s)", apart, ramp)
	}
	p3 := first(ev, is("phase_started", "phase", "P3"))
	for _, phase := range []string{"P1", "P2"} {
		if l := first(ev, is("landed", "phase", phase)); l < 0 || p3 < l {
			t.Errorf("%s landed at event %d, P3 started at %d", phase, l, p3)
		}
	}
	if d := first(ev, is("phase_done", "phase", "P0")); d < 0 || p3 < d {
		t.Errorf("P0 was done at event %d, P3 started at %d", d, p3)
	}
	if first(ev, is("plan_complete")) < 0 {
		t.Fatalf("the plan did not complete:\n%s", eventLog(s))
	}
	log := Git(t, dir, "log", "--format=%s")
	if !strings.Contains(log, "feat: one") || !strings.Contains(log, "feat: two") {
		t.Errorf("the run's branch: %s", log)
	}
	none(t, s, "controller_panic", "land_returned", "workers_off")
}
