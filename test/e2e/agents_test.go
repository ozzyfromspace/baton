//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Phase agents (docs/research/visible-workers.md): baton's mod, loaded with this repository's plugin,
// starts each phase that can go ahead beside the lead as a background subagent of the lead's session, in a
// worktree of baton's. The scenarios read the lead's run (events.jsonl) and each phase's sub-run
// (workers/<phase>/events.jsonl).

// workerPlan has two phases that can go ahead at once: the lead takes P0, and a phase agent P1. P2 needs
// both.
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

// WorkerEvents reads the event log of the run's sub-run on phase (.baton/runs/*/workers/<phase>/events.jsonl).
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

// StartAgents starts the lead on the attached plan, with room for one phase agent beside it (env may
// say more). If the test fails, it ends the session, then logs the run's events, and each phase's
// sub-run's events.
func StartAgents(t *testing.T, dir string, env []string, args ...string) *Session {
	t.Helper()
	s := StartEnv(t, dir, append([]string{"BATON_WORKERS=1"}, env...),
		append([]string{"Start the attached plan, and follow baton's instructions.", "--model", "haiku"}, args...)...)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		s.stop(20 * time.Second) // the phase agents end with the lead's claude
		logEvents(t, "the run", s.Events())
		subs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "workers", "*"))
		for _, sub := range subs {
			logEvents(t, "the phase agent on "+filepath.Base(sub), readEvents(filepath.Join(sub, "events.jsonl")))
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

// signalScript waits until the test writes the file its first argument names into the repository's git
// directory (signal), from a worktree too.
const signalScript = `#!/bin/sh
# Waits until the test writes the file $1 into the repository's git directory, from a worktree too.
common=$(git rev-parse --git-common-dir)
i=0
while [ ! -e "$common/$1" ] && [ "$i" -lt 900 ]; do
	sleep 1
	i=$((i + 1))
done
echo "wait: $1 is there"
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

// signal lets every wait.sh of the project waiting on name end.
func signal(t *testing.T, dir, name string) { writeFile(t, filepath.Join(dir, ".git"), name, "") }

// before reports that the first event cond matches comes before the first one then matches, both there.
func before(events []map[string]any, cond, then func(map[string]any) bool) bool {
	i, j := first(events, cond), first(events, then)
	return i >= 0 && j > i
}

// board is what `baton board` prints for the project's run, from a shell in dir, as JSON.
func board(t *testing.T, dir string) map[string]any {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal([]byte(batonIn(t, dir, t.TempDir(), nil, "board", "--json")), &b); err != nil {
		t.Fatalf("baton board --json: %v", err)
	}
	return b
}

// stand is how the board says phase stands (its stand's kind), or "".
func stand(b map[string]any, phase string) string {
	steps, _ := b["steps"].([]any)
	for _, s := range steps {
		phases, _ := s.(map[string]any)["phases"].([]any)
		for _, p := range phases {
			if p := p.(map[string]any); p["id"] == phase {
				st, _ := p["stand"].(map[string]any)
				return fmt.Sprint(st["kind"])
			}
		}
	}
	return ""
}

// A phase agent takes up P1 beside the lead, which works on P0: in a worktree of its own, placed by baton's
// worktree hook as baton's mod starts it, from its own brief, with the lead's permission rules. Once it
// reports P1 done, its commit lands on the run's branch under the lead, the agent ends, and P2, which needs
// both, starts only then. The lead hears what landed.
func TestAPhaseAgentWorksBesideTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, workerPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	spawned := s.WaitEvent("worker_spawned", 2*time.Minute)
	if spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no phase agent on P1: %v", spawned)
	}
	worktree, _ := spawned["worktree"].(string)
	s.Until("the plan to complete", 8*time.Minute, func() bool {
		return first(s.Events(), is("plan_complete")) >= 0 || first(s.Events(), is("phase_done", "phase", "P2")) >= 0
	})
	ev := s.Events()
	placed := first(ev, is("agent_placed", "phase", "P1", "kept", "false"))
	if placed < 0 || !before(ev, is("mod_hello"), is("agent_placed", "phase", "P1")) || first(ev, is("worker_started", "phase", "P1", "agent", fmt.Sprint(ev[placed]["agent"]))) < 0 {
		t.Fatal("the phase agent did not start in its phase's worktree")
	}
	w := s.WorkerEvents("P1")
	for i, want := range []func(map[string]any) bool{is("phase_started", "phase", "P1"), is("brief", "type", "worker"), is("phase_done", "phase", "P1"), is("worker_finished", "phase", "P1")} {
		if first(w, want) < 0 {
			t.Fatalf("the phase agent's events lack step %d", i)
		}
	}
	landed := first(ev, is("landed", "phase", "P1"))
	if first(ev, is("land_queued", "phase", "P1")) < 0 || landed < 0 {
		t.Fatal("P1's work did not land")
	}
	if p2 := first(ev, is("phase_started", "phase", "P2")); p2 < landed || first(ev, is("phase_done", "phase", "P0")) > p2 {
		t.Errorf("P2 started (event %d) before P0 was done or P1 landed (event %d)", p2, landed)
	}
	if log := Git(t, dir, "log", "--format=%s"); !strings.Contains(log, "feat: one") {
		t.Errorf("the run's branch lacks the phase agent's commit: %s", log)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "one.txt")); err != nil || strings.TrimSpace(string(got)) != "one" {
		t.Errorf("one.txt in the checkout: %q, %v", got, err)
	}
	if first(ev, is("worker_ended", "phase", "P1", "why", "its work landed")) < 0 {
		t.Error("the landed phase agent did not end")
	}
	if _, err := os.Stat(worktree); err == nil {
		t.Errorf("the landed phase agent's worktree %s is still there", worktree)
	}
	if first(ev, is("landed_told")) < 0 {
		t.Error("the lead was never told what landed")
	}
	none(t, s, "serial", "worker_resumed", "controller_panic", "land_returned", "land_deferred", "worktree_made", "agent_unplaced", "worker_deferred")
}

// Where Claude Code does not load baton's mod (an organization turns plugins' mods off), the plan runs in
// the lead, one phase at a time, and baton says why once: no phase agent starts, however many the config
// allows.
func TestWithNoModThePlanRunsInTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, workerPlan)
	s := StartAgents(t, dir, nil, "--plugin-dir", noModPlugin(t), "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	s.Until("the plan to complete", 8*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if first(ev, is("serial")) < 0 || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("the plan did not run serially in the lead:\n%s", eventLog(s))
	}
	for _, phase := range []string{"P0", "P1", "P2"} {
		if first(ev, is("phase_done", "phase", phase)) < 0 {
			t.Errorf("the lead did not do %s", phase)
		}
	}
	if n := count(ev, is("serial")); n != 1 {
		t.Errorf("serial said %d times", n)
	}
	if log := Git(t, dir, "log", "--format=%s"); !strings.Contains(log, "feat: one") {
		t.Errorf("the run's branch: %s", log)
	}
	none(t, s, "mod_hello", "worker_spawned", "controller_panic")
}

// overlapPlan: the lead's P0 holds a change to list.txt uncommitted until the test releases it, while
// the phase agent's P1 changes list.txt too, on another line.
const overlapPlan = `# Overlap plan

## P0 — Commit the first line
After: none

list.txt already has a change of yours that is not committed yet: its first line is ALPHA now. Leave it uncommitted until step 3.

1. Hand baton this wait, exactly: baton await --notes 'the change to list.txt is not committed yet; commit it once the wait ends' -- sh hold.sh
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
3. When baton gives you the result, commit list.txt: git add list.txt, then git commit -m 'feat: alpha'. Then report P0 done: baton done P0 --notes 'alpha committed'

## P1 — Change the last line
After: none

Change the last line of list.txt from epsilon to EPSILON, and leave every other line as it is. Commit it: git add list.txt, then git commit -m 'feat: epsilon'. Then report P1 done: baton done P1 --notes 'epsilon'
`

// The phase agent's work changes list.txt, which the lead has changed and not committed: its landing
// waits, since the fast-forward would refuse it, or worse. Once the lead commits, baton rebases the
// agent's branch onto the lead's commit and lands it, and list.txt keeps both changes.
func TestALandingWaitsForTheLeadsChanges(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, overlapPlan)
	writeFile(t, dir, "list.txt", "ALPHA\nbeta\ngamma\ndelta\nepsilon\n") // the lead's change, not committed
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Edit", "Write")
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

// keepUpPlan: the phase agent's P1 hands baton a gate, which holds on its first two runs (the agent's
// own, and the merge queue's again) until the test lets each go; P2 needs both phases.
const keepUpPlan = `# Keep-up plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand the gate to baton, exactly: baton verify --notes 'one committed' -- sh gate.sh
3. Report P1 done: baton done P1 --notes 'wrote one; the gate runs'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// countedGate counts its runs in the repository's git directory (gate-runs); the first two each say they
// started (gate-started1, 2), then wait until the test lets them go (go1, go2).
const countedGate = `#!/bin/sh
# Counts its runs; the first two each wait until the test lets them go.
common=$(git rev-parse --git-common-dir)
n=$(( $(cat "$common/gate-runs" 2>/dev/null || echo 0) + 1 ))
echo "$n" >"$common/gate-runs"
echo "gate: run $n"
if [ "$n" -le 2 ]; then
	touch "$common/gate-started$n"
	i=0
	while [ ! -e "$common/go$n" ] && [ "$i" -lt 900 ]; do
		sleep 1
		i=$((i + 1))
	done
fi
echo "gate: passed"
`

// The checkout gets a commit of its own while the phase agent's gate runs, and another while the merge
// queue verifies the phase again on its rebased branch. The first verifies it again, once; the second does
// not, however long the gate takes: the phase lands over it, unverified with it, and the lead is told.
func TestALandingKeepsUpWithTheLead(t *testing.T) {
	dir := projectWith(t, map[string]string{"gate.sh": countedGate})
	Attach(t, dir, keepUpPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()
	common := filepath.Join(dir, ".git")
	commit := func(name string) string {
		writeFile(t, dir, name, name+"\n")
		Git(t, dir, "add", name)
		Git(t, dir, "commit", "-q", "-m", "chore: the checkout's "+name)
		return strings.TrimSpace(Git(t, dir, "rev-parse", "HEAD"))
	}
	started := func(n int) {
		t.Helper()
		if !s.Until(fmt.Sprintf("the gate's run %d", n), 8*time.Minute, func() bool {
			_, err := os.Stat(filepath.Join(common, fmt.Sprintf("gate-started%d", n)))
			return err == nil
		}) {
			t.Fatalf("the gate's run %d never started", n)
		}
	}

	started(1)
	commit("first.txt")
	writeFile(t, common, "go1", "")
	again := s.WaitEvent("reverify_queued", 6*time.Minute)
	if again == nil || again["phase"] != "P1" || again["why"] != "lead" {
		t.Fatalf("P1 was not verified again for the checkout's commit: %v", again)
	}
	started(2)
	second := commit("second.txt")
	writeFile(t, common, "go2", "")
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })

	ev := s.Events()
	landed := first(ev, is("landed", "phase", "P1"))
	if landed < 0 {
		t.Fatalf("P1 did not land:\n%s", eventLog(s))
	}
	if got := fmt.Sprint(ev[landed]["unverified"]); got != "["+second+"]" || ev[landed]["verified"] != again["commit"] {
		t.Errorf("landed over %s, verified on %v; want over %s, verified on %v", got, ev[landed]["verified"], second, again["commit"])
	}
	if n := len(slices.DeleteFunc(slices.Clone(ev), func(e map[string]any) bool { return e["kind"] != "reverify_queued" })); n != 1 {
		t.Errorf("verified again %d times, want once", n)
	}
	if runs := strings.TrimSpace(readFile(common, "gate-runs")); runs != "2" {
		t.Errorf("the gate ran %s times, want 2", runs)
	}
	if !before(ev, is("landed", "phase", "P1"), is("landed_told")) {
		t.Error("the lead was never told what landed")
	}
	log := Git(t, dir, "log", "--format=%s")
	if o, f, sc := strings.Index(log, "feat: one"), strings.Index(log, "first.txt"), strings.Index(log, "second.txt"); o < 0 || f < 0 || sc < 0 || o > sc || sc > f {
		t.Errorf("the run's branch, newest first: %s", log)
	}
	none(t, s, "land_returned", "controller_panic", "escalated")
}

// conflictPlan: the phase agent's P1 changes the third line of list.txt, which the run's branch changes
// meanwhile.
const conflictPlan = `# Conflict plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Change the third line
After: none

1. Change the third line of list.txt from gamma to gamma (agent), and leave every other line as it is.
2. Commit it: git add list.txt, then git commit -m 'feat: the agent gamma'.
3. Report P1 done: baton done P1 --notes 'gamma changed'

If baton then says your work conflicts with the run's branch, rebase as it says. In list.txt, keep both versions of the third line: the one from the run's branch first, then yours on the line after it, and leave the other lines as they are.
`

// The run's branch changes the line the phase agent changes, after the agent started: its rebase onto the
// run's branch conflicts, so baton hands the phase back to the agent with a message, and the agent rebases
// and resolves it itself and reports it done again. Then it lands, with both changes.
func TestAConflictGoesBackToThePhaseAgent(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, conflictPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git:*)", "Bash(echo:*)", "Edit", "Write")
	s.AutoApprove()

	if spawned := s.WaitEvent("worker_spawned", 2*time.Minute); spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no phase agent on P1: %v", spawned)
	}
	// Someone in the checkout changes the same line on the run's branch, once the agent's branch is made.
	writeFile(t, dir, "list.txt", "alpha\nbeta\ngamma (lead)\ndelta\nepsilon\n")
	Git(t, dir, "commit", "-q", "-m", "feat: the lead gamma", "list.txt")

	s.Until("the plan to complete", 10*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	returned := first(ev, is("land_returned", "phase", "P1"))
	if returned < 0 || fmt.Sprint(ev[returned]["paths"]) != "[list.txt]" {
		t.Fatal("the conflict did not go back to the phase agent")
	}
	if !before(ev, is("land_returned", "phase", "P1"), is("landed", "phase", "P1")) {
		t.Fatal("P1 never landed after its conflict")
	}
	if !before(ev, is("land_returned", "phase", "P1"), is("mod_command", "command", "send", "phase", "P1")) {
		t.Error("the conflict was not sent to the phase agent")
	}
	if w := s.WorkerEvents("P1"); len(filter(w, is("phase_done", "phase", "P1"))) < 2 {
		t.Error("the phase agent did not report P1 done again")
	}
	log := Git(t, dir, "log", "--format=%s")
	if l, w := strings.Index(log, "feat: the lead gamma"), strings.Index(log, "feat: the agent gamma"); l < 0 || w < 0 || w > l {
		t.Errorf("the run's branch, newest first: %s", log)
	}
	got := readFile(dir, "list.txt")
	if l, w := strings.Index(got, "gamma (lead)"), strings.Index(got, "gamma (agent)"); l < 0 || w < l || strings.Contains(got, "<<<<<<<") ||
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

// relayPlan: the phase agent's P1 proposes what it will do, and does what baton tells it.
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

// startRelay starts relayPlan with a proposal timeout. acceptEdits lets the Write tool through without a
// prompt, so the only dialog is baton's question.
func startRelay(t *testing.T, timeout string) (*Session, string) {
	t.Helper()
	dir := NewProject(t)
	Attach(t, dir, relayPlan)
	s := StartAgents(t, dir, []string{"BATON_ESCALATION_TIMEOUT=" + timeout}, "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)")
	return s, dir
}

// A phase agent's proposal is put to the human by baton's mod, in Claude Code's own dialog in the lead's
// terminal: the lead's model has no part in it. Nobody answers, so at its deadline (never less than a
// minute after it was asked) the mod answers it itself, Go ahead, and no key is typed into any dialog;
// the answer goes to the agent, which does what it proposed, and its work lands.
func TestAPhaseAgentsProposalGoesAheadAtItsDeadline(t *testing.T) {
	s, dir := startRelay(t, "20s")
	waitSequence(t, s, 8*time.Minute,
		is("relay_queued", "phase", "P1", "type", "proposal"),
		is("relay_issued", "phase", "P1", "via", "mod", "timed", "true"),
		is("dialog_open", "tool", "AskUserQuestion", "dialog", "relay"),
		is("mod_answered", "deadline", "true", "answer", "Go ahead"),
		is("relay_answered", "phase", "P1", "by", "timeout", "via", "mod", "answer", "Go ahead"),
		is("relay_applied", "phase", "P1"),
		is("landed", "phase", "P1"),
	)
	ev := s.Events()
	asked, answered := first(ev, is("relay_issued", "phase", "P1")), first(ev, is("relay_answered", "phase", "P1"))
	if d := eventTime(ev[answered]).Sub(eventTime(ev[asked])); d < time.Minute {
		t.Errorf("answered %s after it was asked, under a minute", d)
	}
	if got := strings.TrimSpace(readFile(dir, "greeting.txt")); got != "hello" {
		t.Errorf("greeting.txt in the checkout: %q", got)
	}
	if first(s.WorkerEvents("P1"), is("proposed", "phase", "P1")) < 0 {
		t.Error("the phase agent's own log lacks its proposal")
	}
	none(t, s, "controller_panic", "worker_waiting", "relay_withdrawn", "auto_answered")
}

// The human answers a phase agent's proposal Wait for me: P1 waits for them, its proposal unresolved, and
// nothing is done. /baton run asks them again, and they answer Go ahead: the agent does it, and its work
// lands.
func TestAHeldQuestionIsAskedAgain(t *testing.T) {
	s, dir := startRelay(t, "10m")
	waitSequence(t, s, 6*time.Minute, is("dialog_open", "tool", "AskUserQuestion", "dialog", "relay"))
	time.Sleep(2 * time.Second)
	s.Key("\r") // the human picks the first option: Wait for me
	waitSequence(t, s, 2*time.Minute,
		is("relay_answered", "phase", "P1", "by", "human", "answer", "Wait for me"),
		is("relay_applied", "phase", "P1"),
	)
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	if _, err := os.Stat(filepath.Join(dir, "greeting.txt")); err == nil || first(s.Events(), is("landed")) >= 0 {
		t.Fatal("P1 went ahead while the human held it")
	}
	if got := stand(board(t, dir), "P1"); got != "needs_you" {
		t.Errorf("the board says P1 stands %q", got)
	}

	s.Type("/baton run")
	waitSequence(t, s, 3*time.Minute,
		is("relay_again", "phase", "P1"),
		is("relay_issued", "phase", "P1"), // a fresh ask: "again" marks one the mod lost
	)
	opens := count(s.Events(), is("dialog_open", "tool", "AskUserQuestion", "dialog", "relay"))
	if !s.Until("the question again", 2*time.Minute, func() bool {
		return count(s.Events(), is("dialog_open", "tool", "AskUserQuestion", "dialog", "relay")) > opens || opens >= 2
	}) {
		t.Fatalf("the mod did not ask again:\n%s", eventLog(s))
	}
	time.Sleep(2 * time.Second)
	s.Key("3") // the human picks Go ahead
	waitSequence(t, s, 6*time.Minute,
		is("relay_answered", "phase", "P1", "by", "human", "answer", "Go ahead"),
		is("landed", "phase", "P1"),
	)
	if got := strings.TrimSpace(readFile(dir, "greeting.txt")); got != "hello" {
		t.Errorf("greeting.txt in the checkout: %q", got)
	}
	none(t, s, "controller_panic", "auto_answered")
}

// askPlan: the phase agent's P1 runs a command nobody allowed, so Claude Code asks the human first.
const askPlan = `# Ask plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Count
After: none

1. Run exactly this command with the Bash tool, and nothing else in its place: python3 -c "print(1 + 1)"
2. Write what it printed into a new file named count.txt. Commit it: git add count.txt, then git commit -m 'feat: count'. Then report P1 done: baton done P1 --notes 'counted'
`

// A phase agent's permission prompt shows in the lead's terminal, and baton knows whose it is: the board
// says the phase needs the human, and baton types nothing while it is open. The human allows it, and the
// agent goes on.
func TestAPhaseAgentsPermissionPromptNeedsTheHuman(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, askPlan)
	s := StartAgents(t, dir, nil, "--permission-mode", "acceptEdits", "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)")

	var open map[string]any
	if !s.Until("P1's permission prompt", 6*time.Minute, func() bool {
		i := first(s.Events(), is("dialog_open", "tool", "Bash", "phase", "P1"))
		if i >= 0 {
			open = s.Events()[i]
		}
		return i >= 0
	}) {
		t.Fatalf("no permission prompt of P1's:\n%s", eventLog(s))
	}
	if open["agent"] == nil || open["agent"] == "" {
		t.Errorf("the prompt is not known for the agent's: %v", open)
	}
	if got := stand(board(t, dir), "P1"); got != "needs_you" {
		t.Errorf("the board says P1 stands %q", got)
	}
	time.Sleep(2 * time.Second)
	s.Key("\r") // the human allows it
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	if first(s.Events(), is("landed", "phase", "P1")) < 0 {
		t.Fatalf("P1 did not land:\n%s", eventLog(s))
	}
	if got := strings.TrimSpace(readFile(dir, "count.txt")); got != "2" {
		t.Errorf("count.txt in the checkout: %q", got)
	}
	none(t, s, "controller_panic", "auto_answered")
}

// agentGatePlan: the phase agent's P1 commits, then waits on hold.sh, which the test releases; meanwhile the
// lead finishes P0 and goes on to P2, after a compaction.
const agentGatePlan = `# Gate plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one, then wait
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand baton this wait, exactly: baton await --notes 'one.txt committed; report P1 done once the wait ends' -- sh hold.sh
3. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
4. When baton gives you the result, report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// The phase agent hands baton its wait and ends its turn. The lead finishes P0 and compacts at its
// boundary meanwhile, which is the lead's alone: the agent waits on, untouched, and is not briefed again.
// When the wait ends, baton wakes the idle agent with a message carrying the result, and the agent
// reports P1 done; its work lands.
func TestAGateWakesAnIdlePhaseAgent(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, agentGatePlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	if !s.Until("the phase agent's wait to start", 5*time.Minute, func() bool { return first(s.WorkerEvents("P1"), is("job_started")) >= 0 }) {
		t.Fatalf("the phase agent never handed baton its wait:\n%s", eventLog(s))
	}
	if !s.Until("the lead to start P2", 5*time.Minute, func() bool { return first(s.Events(), is("phase_started", "phase", "P2")) >= 0 }) {
		t.Fatalf("the lead never went on to P2:\n%s", eventLog(s))
	}
	ev := s.Events()
	if !before(ev, is("compact_queued", "next", "P2"), is("compact_finished", "proof", "transcript")) || !before(ev, is("compact_finished"), is("phase_started", "phase", "P2")) {
		t.Errorf("the lead did not compact before P2:\n%s", eventLog(s))
	}
	if w := s.WorkerEvents("P1"); first(w, is("agent_compacted")) >= 0 || first(w, is("rebrief_sent")) >= 0 || first(ev, is("worker_ended", "phase", "P1")) >= 0 {
		t.Error("the lead's compaction touched the phase agent")
	}

	sends := count(s.Events(), is("mod_command", "command", "send", "phase", "P1"))
	release(t, dir)
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	w := s.WorkerEvents("P1")
	if !before(w, is("job_finished", "status", "passed"), is("phase_done", "phase", "P1")) {
		t.Fatalf("P1 was not done after its wait:\n%s", eventLog(s))
	}
	if count(s.Events(), is("mod_command", "command", "send", "phase", "P1")) <= sends {
		t.Error("the idle phase agent was not woken by a message")
	}
	if first(s.Events(), is("landed", "phase", "P1")) < 0 {
		t.Errorf("P1 did not land:\n%s", eventLog(s))
	}
	none(t, s, "controller_panic", "worker_resumed", "escalated")
}

// bigFile is n's file of about 3.5k tokens, for an agent to read: small enough that several fit between two
// compactions. Claude Code keeps a recent stretch of the conversation through a compaction (about 50k
// tokens of the agent's, against a 100k window that compacts at about 67k), and ends a turn whose context
// refills within three turns of each of three compactions in a row ("Autocompact is thrashing").
func bigFile(n int) string {
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa")
	var b strings.Builder
	for j := 0; j < 100; j++ {
		for k := 0; k < 12; k++ {
			b.WriteString(words[(j+k)%16] + " ")
		}
		fmt.Fprintf(&b, "%d.%d\n", n, j)
	}
	return b.String()
}

// compactPlan: P1's agent reads enough to compact (under a small window) once the test signals; P2's agent
// runs a command nobody allowed, whose permission prompt holds the lead's compaction; the lead's P0 waits
// for that prompt, then finishes, and owes a compaction before P3.
const compactPlan = `# Compact plan

## P0 — Say zero
After: none

1. Run exactly this command with the Bash tool: sh wait.sh asked — it waits a while, then says so.
2. Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Read a lot
After: none

1. Run exactly this command with the Bash tool: sh wait.sh read — it waits a while, then says so.
2. Use the Read tool to read the files big1.txt to big16.txt in order, one file per Read call, one call at a time, each in full.
3. Write the single word read into a new file named read.txt. Commit it: git add read.txt, then git commit -m 'feat: read'. Then report P1 done: baton done P1 --notes 'read them'

## P2 — Count
After: none

1. Run exactly this command with the Bash tool, and nothing else in its place: python3 -c "print(1 + 1)"
2. Write what it printed into a new file named count.txt. Commit it: git add count.txt, then git commit -m 'feat: count'. Then report P2 done: baton done P2 --notes 'counted'

## P3 — Say three
After: P0

Run echo three, then report P3 done: baton done P3 --notes 'said three'
`

// A phase agent compacts by itself while the lead owes a compaction at its boundary (held: a permission
// prompt of another agent's is open). The agent's compaction hooks look like the lead's (no agent_id, the
// lead's transcript), and change nothing of the lead's: its compaction is still owed, and P3 does not
// start. Only the agent's own transcript proves its compaction, and the agent is briefed again. Once the
// prompt is answered, the lead compacts, proven by its own transcript, and P3 starts.
func TestAPhaseAgentsCompactionIsNotTheLeads(t *testing.T) {
	files := map[string]string{"wait.sh": signalScript}
	for i := 1; i <= 16; i++ {
		files[fmt.Sprintf("big%d.txt", i)] = bigFile(i)
	}
	dir := projectWith(t, files)
	Attach(t, dir, compactPlan)
	// Claude Code compacts the agent at 100k tokens, and baton's checkpoint does not come first.
	s := StartAgents(t, dir, []string{"BATON_WORKERS=2", "BATON_CHECKPOINT_PCT=99"}, "--autocompact", "100k", "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh wait.sh asked)", "Bash(sh wait.sh read)", "Read")

	if !s.Until("P2's permission prompt", 6*time.Minute, func() bool { return first(s.Events(), is("dialog_open", "tool", "Bash", "phase", "P2")) >= 0 }) {
		t.Fatalf("no permission prompt of P2's:\n%s", eventLog(s))
	}
	signal(t, dir, "asked")
	queued := s.WaitEvent("compact_queued", 4*time.Minute)
	if queued == nil || queued["next"] != "P3" {
		t.Fatalf("the lead owes no compaction before P3: %v", queued)
	}
	signal(t, dir, "read")
	if !s.Until("P1's agent to compact", 8*time.Minute, func() bool { return first(s.WorkerEvents("P1"), is("agent_compacted", "proof", "transcript")) >= 0 }) {
		t.Fatalf("P1's agent never compacted:\n%s", eventLog(s))
	}
	time.Sleep(5 * time.Second) // the lead's hooks for the agent's compaction, and its rewake, have had their say
	ev := s.Events()
	if first(ev, is("compact_seen", "hook", "SessionStart")) < 0 {
		t.Error("the agent's compaction fired none of the lead's hooks")
	}
	if first(ev, is("compact_finished")) >= 0 || first(ev, is("phase_started", "phase", "P3")) >= 0 || first(ev, is("rewake")) >= 0 {
		t.Fatalf("the agent's compaction was taken for the lead's:\n%s", eventLog(s))
	}
	var st struct {
		BoundaryOwed bool `json:"boundary_owed"`
	}
	runs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "state.json"))
	if len(runs) != 1 || json.Unmarshal([]byte(readFile(filepath.Dir(runs[0]), "state.json")), &st) != nil || !st.BoundaryOwed {
		t.Errorf("the lead's boundary is no longer owed (%d runs)", len(runs))
	}

	s.Key("\r") // the human allows P2's command
	waitSequence(t, s, 6*time.Minute,
		is("compact_finished", "proof", "transcript"),
		is("phase_started", "phase", "P3"),
		kind("plan_complete"),
	)
	w := s.WorkerEvents("P1")
	if !before(w, is("agent_compacted"), is("brief", "type", "auto", "phase", "P1")) && !before(w, is("agent_compacted"), is("rebrief_sent")) {
		t.Errorf("P1's agent was not briefed again after its compaction")
	}
	none(t, s, "controller_panic", "auto_answered", "compaction_failed")
}

// dropPlan: the phase agent's P1 commits, then waits on hold.sh, which the test never releases.
const dropPlan = `# Drop plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one, then wait
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand baton this wait, exactly: baton await --notes 'one.txt committed; report P1 done once the wait ends' -- sh hold.sh
3. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
4. When baton gives you the result, report P1 done: baton done P1 --notes 'wrote one'
`

// workerHolds waits, in a run of dropPlan, until the phase agent on P1 has committed and waits on hold.sh,
// and the lead has finished P0 and is quiet. It returns the agent's worktree and branch, and hold.sh's pid.
func workerHolds(t *testing.T, s *Session, dir string) (worktree, branch string, hold int) {
	t.Helper()
	spawned := s.WaitEvent("worker_spawned", 2*time.Minute)
	if spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no phase agent on P1: %v", spawned)
	}
	worktree, branch = fmt.Sprint(spawned["worktree"]), fmt.Sprint(spawned["branch"])
	if !s.Until("the phase agent's wait to start", 5*time.Minute, func() bool { return first(s.WorkerEvents("P1"), is("job_started")) >= 0 }) {
		t.Fatal("the phase agent never handed baton its wait")
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

// Dropping the run while its phase agent works ends the agent: it is stopped, its job stops, and its
// worktree is removed, but its branch stays, since it holds a commit that never landed.
func TestDropEndsThePhaseAgents(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, dropPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	worktree, branch, hold := workerHolds(t, s, dir)
	s.Type("Run exactly this one command with the Bash tool: baton drop\nThen reply DONE.")
	if s.WaitEvent("dropped", 2*time.Minute) == nil {
		t.Fatal("the run was not dropped")
	}
	var ended map[string]any
	s.Until("the phase agent to end", time.Minute, func() bool {
		i := first(s.Events(), is("worker_ended", "phase", "P1"))
		if i >= 0 {
			ended = s.Events()[i]
		}
		return i >= 0
	})
	// A dropped run's branch is kept under kept/, so no later agent on P1 starts from its commits.
	kept := fmt.Sprint(ended["branch"])
	if ended == nil || ended["why"] != "the run was dropped" || !strings.HasPrefix(kept, strings.TrimSuffix(branch, "P1")+"kept/P1-") || fmt.Sprint(ended["unlanded"]) != "1" {
		t.Fatalf("worker_ended: %v", ended)
	}
	if first(s.Events(), is("mod_command", "command", "stop", "phase", "P1")) < 0 {
		t.Error("the phase agent was not stopped")
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree %s is still there: %v", worktree, err)
	}
	if log := Git(t, dir, "log", "--format=%s", kept); !strings.Contains(log, "feat: one") {
		t.Errorf("the kept branch %s lost its commit: %s", kept, log)
	}
	if log := Git(t, dir, "log", "--format=%s"); strings.Contains(log, "feat: one") {
		t.Errorf("the dropped agent's commit landed: %s", log)
	}
	if !s.Until("hold.sh to stop", time.Minute, func() bool { return syscall.Kill(hold, 0) != nil }) {
		t.Errorf("the phase agent's job, hold.sh (pid %d), still runs", hold)
	}
	none(t, s, "landed", "controller_panic", "worker_resumed")
}

// crashPlan's agent runs slow.sh, which takes half a minute and records each run of it, while the lead's
// P0 waits on hold.sh.
const agentCrashPlan = `# Agent crash plan

## P0 — Wait for the release
After: none

1. Hand baton this wait, exactly: baton await --notes 'waiting for the release' -- sh hold.sh
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
3. When baton gives you the wait's result, report P0 done: baton done P0 --notes 'released'

## P1 — Take a while
After: none

1. Run exactly this command with the Bash tool: sh slow.sh — it takes about half a minute, and says when it is done.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'slow.sh ran'
`

// The lead's claude is killed while a phase agent works. baton brings the lead's conversation back, and
// the agent with it: by a message to the same agent, which goes on with its conversation in its worktree.
// Its work lands, and the plan completes.
func TestAPhaseAgentComesBackWithTheLead(t *testing.T) {
	dir := projectWith(t, map[string]string{"slow.sh": slowScript, "hold.sh": holdScript})
	Attach(t, dir, agentCrashPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(sh slow.sh)", "Write")
	s.AutoApprove()
	if !s.Until("slow.sh to start", 4*time.Minute, func() bool { return readFile(filepath.Join(dir, ".git"), "slow-runs") != "" }) {
		t.Fatalf("slow.sh never ran:\n%s", eventLog(s))
	}
	started := s.WaitEvent("worker_started", time.Minute)
	pid, _ := claudeOf(t, s.cmd.Process.Pid)
	if pid == 0 || started == nil {
		t.Fatalf("no claude under baton, or no agent at work: %v", started)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	waitSequence(t, s, 5*time.Minute,
		is("lead_exited", "code", "137", "then", "resume"),
		is("worker_ended", "phase", "P1", "suspended", "true"),
		kind("host_started"),
		is("worker_resumed", "phase", "P1", "by", "message", "agent", fmt.Sprint(started["agent"])),
		is("landed", "phase", "P1"),
	)
	release(t, dir)
	s.Until("the plan to complete", 4*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	if first(s.Events(), is("plan_complete")) < 0 {
		t.Fatalf("the plan did not complete:\n%s", eventLog(s))
	}
	if n := count(s.Events(), is("worker_spawned")); n != 1 {
		t.Errorf("%d agents started for P1, want the one, resumed", n)
	}
	none(t, s, "controller_panic", "escalated")
}

// clearPlan: both the lead's P0 and the phase agent's P1 wait on hold.sh.
const clearPlan = `# Clear plan

## P0 — Wait for the release
After: none

1. Hand baton this wait, exactly: baton await --notes 'waiting for the release' -- sh hold.sh
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it. Meanwhile, do what the human asks, and end your turn when that is done.
3. When baton gives you the wait's result, report P0 done: baton done P0 --notes 'released'

## P1 — Write one, then wait
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand baton this wait, exactly: baton await --notes 'one.txt committed; report P1 done once the wait ends' -- sh hold.sh
3. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
4. When baton gives you the result, report P1 done: baton done P1 --notes 'wrote one'
`

// The human runs /clear in the lead while its phase agent waits: /clear ends every agent of the session,
// and none can be started again. baton takes P1 up with a fresh agent in its worktree, told to take the
// phase up where it stands; the lead's fresh conversation gets its own phase's brief, if it still holds it
// (it may have reported P0 done while its wait runs). Both finish once the wait ends, and P1's work lands.
func TestAClearGivesThePhaseAFreshAgent(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, clearPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()
	if !s.Until("both waits", 5*time.Minute, func() bool {
		return first(s.WorkerEvents("P1"), is("job_started")) >= 0 && first(s.Events(), is("idle_on_job")) >= 0
	}) {
		t.Fatalf("the lead and its agent never both waited:\n%s", eventLog(s))
	}
	started := s.WaitEvent("worker_started", time.Minute)
	s.WaitQuiet(5*time.Second, time.Minute)

	held := first(s.Events(), is("phase_verifying", "phase", "P0")) < 0 // the lead may report P0 done while its wait runs
	s.Type("/clear")
	if held {
		waitSequence(t, s, time.Minute,
			is("session_start", "source", "clear"),
			is("brief", "type", "fresh", "phase", "P0"),
		)
	}
	waitSequence(t, s, 3*time.Minute,
		is("session_start", "source", "clear"),
		is("worker_ended", "phase", "P1", "suspended", "true"),
		func(e map[string]any) bool {
			return e["kind"] == "worker_resumed" && e["phase"] == "P1" && e["by"] == nil
		},
		func(e map[string]any) bool {
			return e["kind"] == "worker_started" && e["phase"] == "P1" && e["agent"] != started["agent"]
		},
	)
	if first(s.WorkerEvents("P1"), is("agent_cleared")) < 0 {
		t.Error("the sub-run does not say its agent went with the /clear")
	}
	release(t, dir)
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	if first(s.Events(), is("landed", "phase", "P1")) < 0 || first(s.Events(), is("plan_complete")) < 0 {
		t.Fatalf("the plan did not complete:\n%s", eventLog(s))
	}
	if log := Git(t, dir, "log", "--format=%s"); strings.Count(log, "feat: one") != 1 {
		t.Errorf("the run's branch: %s", log)
	}
	none(t, s, "controller_panic", "escalated")
}

// autoPlan is three phases that need nothing from each other, and one that needs them all. The lead's P0
// waits on hold.sh until the test releases it, so the lead takes no other phase meanwhile, and the agents'
// P1 and P2 wait until the test signals both.
const autoPlan = `# Auto plan

## P0 — Wait
After: none

1. Hand baton this wait, exactly: baton await --notes 'nothing to commit; report P0 done once the wait ends' -- sh hold.sh
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
3. When baton gives you the result, report P0 done: baton done P0 --notes 'waited'

## P1 — Write one
After: none

1. Run exactly this command with the Bash tool: sh wait.sh both — it waits a while, then says so.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Write two
After: none

1. Run exactly this command with the Bash tool: sh wait.sh both — it waits a while, then says so.
2. Write the single word two into a new file named two.txt. Commit it: git add two.txt, then git commit -m 'feat: two'. Then report P2 done: baton done P2 --notes 'wrote two'

## P3 — Say three
After: all

Run echo three, then report P3 done: baton done P3 --notes 'said three'
`

// With workers: auto, the plan decides how many phase agents work at once: P1 and P2 can go ahead beside
// the lead's P0, so two start, one after the other, on a machine with room for them. baton's board shows
// them both at work in the plan's first step. Both land, and P3, which needs every phase, starts after.
// No agent starts for P3: by then nothing else can go ahead.
func TestTwoPhaseAgentsWorkBesideTheLead(t *testing.T) {
	dir := projectWith(t, map[string]string{"hold.sh": holdScript, "wait.sh": signalScript})
	Attach(t, dir, autoPlan)
	s := StartAgents(t, dir, []string{"BATON_WORKERS=auto", "BATON_SWITCH_AFTER=10m"},
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh wait.sh both)", "Write")
	s.AutoApprove()

	if !s.Until("two phase agents at work", 4*time.Minute, func() bool { return count(s.Events(), is("worker_started")) >= 2 }) {
		t.Fatalf("fewer than two phase agents started:\n%s", eventLog(s))
	}
	time.Sleep(10 * time.Second) // both are at work, waiting on wait.sh
	b := board(t, dir)
	band := strings.TrimSpace(batonIn(t, dir, t.TempDir(), nil, "board", "--band"))
	if b["step"] != 1.0 || !strings.Contains(band, "step 1/2") {
		t.Errorf("the board's step: %v; the band: %q", b["step"], band)
	}
	for _, phase := range []string{"P1", "P2"} {
		if got := stand(b, phase); got != "agent" {
			t.Errorf("the board says %s stands %q", phase, got)
		}
		if !strings.Contains(band, phase+" ⚙") {
			t.Errorf("the band lacks %s at work: %q", phase, band)
		}
	}
	signal(t, dir, "both")
	release(t, dir)
	s.Until("the plan to complete", 8*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	spawns := filter(ev, is("worker_spawned"))
	if len(spawns) != 2 || spawns[0]["phase"] != "P1" || spawns[1]["phase"] != "P2" {
		t.Fatalf("phase agents started: %v", spawns)
	}
	p3 := first(ev, is("phase_started", "phase", "P3"))
	for _, phase := range []string{"P1", "P2"} {
		if l := first(ev, is("landed", "phase", phase)); l < 0 || p3 < l {
			t.Errorf("%s landed at event %d, P3 started at %d", phase, l, p3)
		}
		if first(s.WorkerEvents(phase), is("brief", "type", "worker")) < 0 {
			t.Errorf("the agent on %s was not briefed", phase)
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
	none(t, s, "controller_panic", "land_returned", "serial", "worker_deferred")
}
