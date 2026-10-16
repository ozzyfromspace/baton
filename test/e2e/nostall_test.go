//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run that never stalls silently (v0.8.2). On 2026-10-13 a real run stopped for hours: a phase agent's
// gate failed after its `baton done` and nothing brought the agent back; baton's Ctrl-C, clearing the
// human's draft for a compaction, ended a phase agent; a compaction the human ran while paused did not
// count; agents' reports reached the lead as if the human had written them; and a turn the human started
// was cut short. Each scenario here replays one of those on real Claude Code, as v0.8.2 handles it.

// startedWait says it started (started-NAME in the repository's git directory), then waits until the test
// writes NAME there (signal), from a worktree or a lane too.
const startedWait = `#!/bin/sh
# Says it started, then waits until the test writes the file $1 into the repository's git directory.
common=$(git rev-parse --git-common-dir)
touch "$common/started-$1"
i=0
while [ ! -e "$common/$1" ] && [ "$i" -lt 900 ]; do
	sleep 1
	i=$((i + 1))
done
echo "wait: $1 is there"
`

// waitStarted waits until the project's wait.sh NAME has started.
func waitStarted(t *testing.T, s *Session, dir, name string, timeout time.Duration) {
	t.Helper()
	if !s.Until("wait.sh "+name+" to start", timeout, func() bool {
		_, err := os.Stat(filepath.Join(dir, ".git", "started-"+name))
		return err == nil
	}) {
		t.Fatalf("wait.sh %s never started:\n%s", name, eventLog(s))
	}
}

// runState is the project's one run's state.json.
func runState(t *testing.T, dir string) map[string]any {
	t.Helper()
	runs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "state.json"))
	var st map[string]any
	if len(runs) != 1 || json.Unmarshal([]byte(readFile(filepath.Dir(runs[0]), "state.json")), &st) != nil {
		t.Fatalf("no one run's state: %v", runs)
	}
	return st
}

// gateRuns is how many times the project's gate ran, as it counts them in the repository's git directory.
func gateRuns(dir string) string {
	return strings.TrimSpace(readFile(filepath.Join(dir, ".git"), "gate-runs"))
}

// markerGate is a phase's gate: green only when the file marker holds the single word ok. It takes half a
// minute, and counts its runs in the repository's git directory (gate-runs).
const markerGate = `#!/bin/sh
# The phase's gate: green only when marker holds the single word ok. Each run is counted.
common=$(git rev-parse --git-common-dir)
n=$(( $(cat "$common/gate-runs" 2>/dev/null || echo 0) + 1 ))
echo "$n" >"$common/gate-runs"
echo "gate: checking"
sleep 30
if [ "$(cat marker 2>/dev/null)" = ok ]; then
	echo "gate: passed"
	exit 0
fi
echo "gate: FAILED: the file marker must hold the single word ok, and it holds: $(cat marker 2>/dev/null || echo nothing)"
exit 1
`

// agentRedPlan: the phase agent's P1 commits a marker its gate turns down, and is reported done while the
// gate runs. P2 needs both phases.
const agentRedPlan = `# Agent red plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Mark the work
After: none

1. Write the single word draft into a new file named marker, and commit it: git add marker, then git commit -m 'feat: a first marker'. Write exactly draft: the gate decides what marker must hold, and says so if it fails.
2. Hand the gate to baton, exactly: baton verify --notes 'marker committed as draft; the gate decides what it must hold' -- sh gate.sh
3. Report P1 done right away, while the gate runs: baton done P1 --notes 'marker committed; the gate runs'. Then end your turn. Do not run the gate yourself, and do not read it.

If baton brings P1 back because the gate failed, do what the gate's output says, commit it (git add marker, then git commit -m 'fix: the marker'), hand the gate to baton again as in step 2 (with notes saying what you fixed), and report P1 done again.

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// The phase agent reports P1 done while its gate runs, and the gate fails. P1 reopens in the agent's
// sub-run at once, owing no compaction there (nothing compacts a phase agent), and its agent is given the
// result: by a message, or in a fresh agent's brief. It fixes the marker, verifies again and reports P1
// done again; the gate passes, and the work lands. (On v0.8.1 the sub-run owed a compaction that never
// came, every call of the agent's was refused, and the run stalled.)
func TestAnAgentsRedGateBringsItsAgentBack(t *testing.T) {
	dir := projectWith(t, map[string]string{"gate.sh": markerGate})
	Attach(t, dir, agentRedPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(printf:*)", "Write", "Edit")
	s.AutoApprove()

	s.Until("the plan to complete", 12*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev, w := s.Events(), s.WorkerEvents("P1")
	failed := first(w, is("job_finished", "phase", "P1", "status", "failed"))
	if failed < 0 || !before(w, is("phase_verifying", "phase", "P1"), is("job_finished", "phase", "P1", "status", "failed")) {
		t.Fatalf("P1's gate did not fail after its done:\n%s", eventLog(s))
	}
	if first(w[failed:], is("phase_resumed", "phase", "P1", "reason", "red")) < 0 {
		t.Fatal("the agent's sub-run did not take P1 up again")
	}
	if n := count(w[failed:], is("phase_done", "phase", "P1", "by", "verification")); n != 1 {
		t.Fatalf("P1 was done by its verification %d times after the red one", n)
	}
	told := eventTime(w[failed])
	if count(filter(ev, func(e map[string]any) bool { return eventTime(e).After(told) }), func(e map[string]any) bool {
		return (e["kind"] == "mod_command" && e["command"] == "send" || e["kind"] == "worker_spawned") && e["phase"] == "P1"
	}) == 0 {
		t.Error("nothing reached the agent after its gate failed")
	}
	if first(ev, is("landed", "phase", "P1")) < 0 || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("P1 did not land:\n%s", eventLog(s))
	}
	if got := strings.TrimSpace(readFile(dir, "marker")); got != "ok" || gateRuns(dir) != "2" {
		t.Errorf("marker in the checkout: %q; the gate ran %s times", got, gateRuns(dir))
	}
	if first(w, is("compact_queued")) >= 0 {
		t.Errorf("the agent's sub-run queued a compaction:\n%v", w)
	}
	none(t, s, "controller_panic", "worker_held", "run_stalled", "escalated")
}

// flakyGate counts its runs in the repository's git directory (gate-runs): its first waits until the test
// lets it go (go1), then passes; its second fails, whatever the work holds; every later run passes.
const flakyGate = `#!/bin/sh
# Counts its runs: the first waits until the test lets it go, the second fails, and the rest pass.
common=$(git rev-parse --git-common-dir)
n=$(( $(cat "$common/gate-runs" 2>/dev/null || echo 0) + 1 ))
echo "$n" >"$common/gate-runs"
echo "gate: run $n"
case $n in
1)
	touch "$common/gate-started1"
	i=0
	while [ ! -e "$common/go1" ] && [ "$i" -lt 900 ]; do
		sleep 1
		i=$((i + 1))
	done
	;;
2)
	echo "gate: FAILED: the gate itself failed this once, and nothing in the work needs fixing. Hand the gate to baton again exactly as before, then report P1 done again."
	exit 1
	;;
esac
echo "gate: passed"
`

// reverifyPlan: the phase agent's P1 hands baton its gate, which the merge queue runs again before it
// lands the work.
const reverifyPlan = `# Reverify plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand the gate to baton, exactly: baton verify --notes 'one committed' -- sh gate.sh
3. Report P1 done: baton done P1 --notes 'wrote one; the gate runs'

If baton brings P1 back because the gate failed, do as the gate's output says: hand the gate to baton again exactly as in step 2, then report P1 done again as in step 3.

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// The checkout gets a commit while the phase agent's gate runs, so the merge queue verifies P1 again with
// the run's branch merged in, and that verification fails. P1 goes back to its agent, owing no compaction
// in its sub-run, and the agent is given the result; it verifies again, reports P1 done again, and the
// work lands.
func TestALandingsRedVerificationGoesBackToTheAgent(t *testing.T) {
	dir := projectWith(t, map[string]string{"gate.sh": flakyGate})
	Attach(t, dir, reverifyPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()
	common := filepath.Join(dir, ".git")
	if !s.Until("the gate's first run", 8*time.Minute, func() bool {
		_, err := os.Stat(filepath.Join(common, "gate-started1"))
		return err == nil
	}) {
		t.Fatalf("the gate never started:\n%s", eventLog(s))
	}
	writeFile(t, dir, "first.txt", "first\n")
	Git(t, dir, "add", "first.txt")
	Git(t, dir, "commit", "-q", "-m", "chore: the checkout's first.txt")
	writeFile(t, common, "go1", "")

	waitSequence(t, s, 10*time.Minute,
		is("reverify_queued", "phase", "P1", "why", "lead"),
		is("land_returned", "phase", "P1"),
		is("landed", "phase", "P1"),
		kind("plan_complete"),
	)
	ev, w := s.Events(), s.WorkerEvents("P1")
	returned := ev[first(ev, is("land_returned", "phase", "P1"))]
	if why := fmt.Sprint(returned["why"]); !strings.Contains(why, "verification failed") {
		t.Errorf("P1 went back for %q", why)
	}
	failed := first(w, is("job_finished", "phase", "P1", "status", "failed"))
	if failed < 0 || first(w[failed:], is("phase_resumed", "phase", "P1", "reason", "red")) < 0 || first(w[failed:], is("phase_done", "phase", "P1")) < 0 {
		t.Fatalf("the agent's sub-run did not take P1 up again and finish it:\n%v", w)
	}
	at := eventTime(returned)
	if count(filter(ev, func(e map[string]any) bool { return eventTime(e).After(at) }), func(e map[string]any) bool {
		return (e["kind"] == "mod_command" && e["command"] == "send" || e["kind"] == "worker_spawned") && e["phase"] == "P1"
	}) == 0 {
		t.Error("nothing reached the agent after its landing went back")
	}
	if gateRuns(dir) != "3" {
		t.Errorf("the gate ran %s times, want 3", gateRuns(dir))
	}
	if first(w, is("compact_queued")) >= 0 {
		t.Errorf("the agent's sub-run queued a compaction:\n%v", w)
	}
	if log := Git(t, dir, "log", "--format=%s"); !strings.Contains(log, "feat: one") || !strings.Contains(log, "first.txt") {
		t.Errorf("the run's branch, newest first: %s", log)
	}
	none(t, s, "controller_panic", "worker_held", "run_stalled", "escalated")
}

// draftPlan: the lead's P0 and the phase agent's P1 each wait in a command until the test lets them go;
// P2 needs only P0, so the lead compacts before it while the agent still waits.
const draftPlan = `# Draft plan

## P0 — Say zero
After: none

1. Run exactly this command with the Bash tool, with its timeout set to 600000 ms: sh wait.sh lead — it waits a while, then says so.
2. Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

1. Run exactly this command with the Bash tool, with its timeout set to 600000 ms: sh wait.sh agent — it waits a while, then says so.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// draftText is what the human leaves in the input box, unsent.
const draftText = "Write the single word kestrel into a new file named scratch/draft.txt with the Write tool."

// The human leaves a draft in the input box while the lead finishes P0 and its phase agent works on P1 in a
// command. The lead compacts at its boundary all the same, through baton's mod: no key is written into the
// session, the draft stays in the box, and the agent goes on untouched. (On v0.8.1 baton cleared the draft
// with Ctrl-C, which also stopped the agent for good.) Once the plan is done, the human sends the draft,
// and the model does what it says.
func TestADraftStaysThroughACompactionWithAnAgentAtWork(t *testing.T) {
	dir := projectWith(t, map[string]string{"wait.sh": startedWait})
	Attach(t, dir, draftPlan)
	s := StartAgents(t, dir, []string{"BATON_FOREGROUND_MAX=10m"}, "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh wait.sh lead)", "Bash(sh wait.sh agent)", "Write")
	waitStarted(t, s, dir, "agent", 5*time.Minute)
	waitStarted(t, s, dir, "lead", 3*time.Minute)
	agent := s.WaitEvent("worker_started", time.Minute)
	time.Sleep(3 * time.Second)
	s.Draft(draftText)
	time.Sleep(3 * time.Second)
	signal(t, dir, "lead")

	waitSequence(t, s, 5*time.Minute,
		is("phase_done", "phase", "P0"),
		is("compact_queued", "reason", "boundary", "next", "P2"),
		is("mod_command", "command", "compact"),
		kind("compact_asked"),
		is("compact_started", "by_baton", "true"),
		is("phase_started", "phase", "P2"),
		kind("rewake"),
	)
	// The agent is still in its command, its turn running: what a Ctrl-C would have stopped.
	if first(s.Events(), is("mod_turn", "agent", fmt.Sprint(agent["agent"]))) >= 0 || first(s.Events(), is("worker_ended", "phase", "P1")) >= 0 {
		t.Fatalf("the agent's turn ended before the test let it go:\n%s", eventLog(s))
	}
	signal(t, dir, "agent")
	s.Until("the plan to complete", 5*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if first(ev, is("landed", "phase", "P1")) < 0 || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("P1 did not land:\n%s", eventLog(s))
	}
	if n := count(ev, is("worker_spawned")); n != 1 || first(ev, is("worker_started", "phase", "P1", "agent", fmt.Sprint(agent["agent"]))) < 0 {
		t.Errorf("%d agents started for P1, want the one, at work through the compaction", n)
	}
	s.Until("the landed agent to end", 30*time.Second, func() bool { return first(s.Events(), is("worker_ended", "phase", "P1")) >= 0 })
	if ended := filter(s.Events(), is("worker_ended", "phase", "P1")); len(ended) != 1 || ended[0]["why"] != "its work landed" {
		t.Errorf("the agent ended: %v", ended)
	}
	for _, e := range ev {
		if e["via"] == "keys" {
			t.Errorf("baton typed into the session: %v", e)
		}
	}
	none(t, s, "draft_rescued", "compact_typed", "worker_held", "controller_panic", "compact_failed")

	s.WaitQuiet(5*time.Second, 2*time.Minute)
	s.Key("\r") // the human sends the draft
	if !s.Until("the draft's file", 3*time.Minute, func() bool { return strings.TrimSpace(readFile(dir, "scratch/draft.txt")) == "kestrel" }) {
		t.Errorf("the draft did not reach the model whole: scratch/draft.txt holds %q", readFile(dir, "scratch/draft.txt"))
	}
}

// pausePlan is two phases, one after the other.
const pausePlan = `# Pause plan

## P0 — Say hello
After: none

Run echo hello, then report P0 done: baton done P0 --notes 'said hello'

## P1 — Say goodbye
After: P0

Run echo goodbye, then report P1 done: baton done P1 --notes 'said goodbye'
`

// The human pauses the run, finishes P0 by hand, and compacts with their own /compact: the compaction P0's
// boundary owed. /baton run counts it, and P1 starts from its brief with no compaction of baton's. (On
// v0.8.1 a compaction while paused did not count, and baton compacted again.)
func TestACompactionWhilePausedIsTheBoundarys(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, pausePlan)
	s := Start(t, dir, "--model", "haiku", "Run exactly this one command with the Bash tool: baton pause\nThen reply with the single word paused.")
	s.Trust()
	s.WaitEvent("paused", 2*time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("Run exactly this one command with the Bash tool: baton done P0 --notes 'said hello'\nThen reply with the single word done.")
	s.WaitEvent("phase_done", 2*time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("/compact")
	s.WaitEvent("compact_finished", 3*time.Minute)
	s.WaitQuiet(5*time.Second, time.Minute)
	if n := count(s.Events(), is("phase_started", "phase", "P1")); n != 0 {
		t.Fatalf("P1 started while paused:\n%s", eventLog(s))
	}
	s.Type("/baton run")
	waitSequence(t, s, 5*time.Minute,
		is("compaction_kept", "phase", "P1", "reason", "boundary"),
		is("phase_started", "phase", "P1"),
		is("brief", "type", "boundary", "phase", "P1"),
		is("phase_done", "phase", "P1"),
		kind("plan_complete"),
	)
	ev := s.Events()
	if n := count(ev, kind("compact_started")); n != 1 {
		t.Errorf("%d compactions, want the human's alone:\n%s", n, eventLog(s))
	}
	none(t, s, "compact_queued", "compact_asked", "compact_typed", "escalated")
}

// pausedNextPlan is a plan to attach in place of pausePlan.
const pausedNextPlan = `# Next plan

## P0 — Say three
After: none

Run echo three, then report P0 done: baton done P0 --notes 'said three'
`

// attachedNext matches the event of next.md's attach.
func attachedNext(e map[string]any) bool {
	return e["kind"] == "attached" && strings.HasSuffix(fmt.Sprint(e["plan"]), "next.md")
}

// The human pauses the run, then attaches another plan in its place: the run stays paused, and the plan it
// replaces is kept. Only /baton run starts the new plan, after a compaction.
func TestAttachingWhilePausedStaysPaused(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, pausePlan)
	commitPlan(t, dir, "next.md", pausedNextPlan)
	s := Start(t, dir, "--model", "haiku", "Run exactly this one command with the Bash tool: baton pause\nThen reply with the single word paused.")
	s.Trust()
	s.WaitEvent("paused", 2*time.Minute)
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("Run exactly this one command with the Bash tool: baton attach next.md --suggested --replace\nThen reply with the single word attached.")
	var attached map[string]any
	if !s.Until("next.md attached", 3*time.Minute, func() bool {
		i := first(s.Events(), attachedNext)
		if i >= 0 {
			attached = s.Events()[i]
		}
		return i >= 0
	}) {
		t.Fatalf("next.md was not attached:\n%s", eventLog(s))
	}
	if attached["paused"] != true {
		t.Errorf("attached: %v", attached)
	}
	archived := s.WaitEvent("plan_archived", time.Minute)
	if archived == nil || readFile(fmt.Sprint(archived["dir"]), "plan.json") == "" || readFile(fmt.Sprint(archived["dir"]), "state.json") == "" {
		t.Errorf("the replaced plan was not kept: %v", archived)
	}
	s.WaitQuiet(10*time.Second, 2*time.Minute)
	at := first(s.Events(), attachedNext)
	if mode := runState(t, dir)["mode"]; mode != "paused" || first(s.Events()[at:], kind("phase_started")) >= 0 {
		t.Fatalf("the run is %v after the attach:\n%s", mode, eventLog(s))
	}
	s.Type("/baton run")
	waitSequence(t, s, 5*time.Minute,
		kind("resumed"),
		is("compact_started", "by_baton", "true"),
		is("phase_started", "phase", "P0"),
		is("phase_done", "phase", "P0"),
		kind("plan_complete"),
	)
	none(t, s, "escalated", "compact_failed")
}

// In auto mode Claude Code hands each phase agent's report back to the lead as a message from another
// session, framed as the agent's. baton's mod drops it, so the lead's model never sees it, and baton never
// takes it for the human's: no turn the human started, and no decisions told. (On v0.8.1 the hand-back
// reached the lead, and baton told it its decisions as if the human had written.)
func TestAnAgentsHandBackNeverReachesTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, workerPlan)
	s := StartAgents(t, dir, nil, "--permission-mode", "auto", "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	s.Until("the plan to complete", 8*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if first(ev, is("landed", "phase", "P1")) < 0 || first(ev, is("plan_complete")) < 0 {
		t.Fatalf("the plan did not complete:\n%s", eventLog(s))
	}
	spawned := ev[first(ev, is("worker_started", "phase", "P1"))]
	if !s.Until("the agent's hand-back to be dropped", time.Minute, func() bool {
		return first(s.Events(), is("mod_dropped", "agent", fmt.Sprint(spawned["agent"]))) >= 0
	}) {
		t.Fatalf("the mod dropped no report of the agent's:\n%s", eventLog(s))
	}
	if n := count(s.Events(), is("turn_started", "by", "human")); n != 1 {
		t.Errorf("%d turns the human started, want the first alone:\n%s", n, eventLog(s))
	}
	none(t, s, "decisions_told", "controller_panic", "serial")
}

// humanTurnPlan: P0 is reported done while its gate runs, and P1 needs it, so the lead holds no phase until
// the gate passes.
const humanTurnPlan = `# Human turn plan

## P0 — Mark the work
After: none

1. Write the single word ok into a new file named marker, and commit it: git add marker, then git commit -m 'feat: the marker'.
2. Hand the gate to baton, exactly: baton verify --notes 'marker committed; the gate runs' -- sh wait.sh gate
3. Report P0 done right away, while the gate runs: baton done P0 --notes 'marker committed; the gate runs'. Then end your turn. Do not run the gate yourself, and do not wait for it.

## P1 — Say one
After: P0

Run echo one, then report P1 done: baton done P1 --notes 'said one'
`

// The human asks for something while P0's gate runs, and the gate passes in the middle of their turn: P1 is
// owed a compaction, which waits for the turn's end. Nothing in the turn is refused (the model writes the
// file the human asked for after the result came), the model is told once that P1 comes after this turn,
// and baton compacts at its stop. (On v0.8.1 every call left in the turn was refused.)
func TestAGateResultInTheHumansTurnWaitsForItsEnd(t *testing.T) {
	dir := projectWith(t, map[string]string{"wait.sh": startedWait})
	Attach(t, dir, humanTurnPlan)
	s := StartEnv(t, dir, []string{"BATON_FOREGROUND_MAX=10m"}, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh wait.sh human)", "Write")
	s.Trust()
	waitSequence(t, s, 4*time.Minute, is("phase_verifying", "phase", "P0"))
	waitStarted(t, s, dir, "gate", 2*time.Minute)
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	s.Type("Run exactly this command with the Bash tool: sh wait.sh human — it waits a while, then says so. When it has, write the single word finished into a new file named scratch/finished.txt with the Write tool, then reply with the single word finished.")
	waitStarted(t, s, dir, "human", 2*time.Minute)
	signal(t, dir, "gate")
	waitSequence(t, s, 2*time.Minute,
		is("job_finished", "phase", "P0", "status", "passed"),
		is("phase_done", "phase", "P0", "by", "verification"),
		is("compact_queued", "reason", "boundary", "next", "P1", "by", "settle"),
	)
	time.Sleep(10 * time.Second)
	if first(s.Events(), kind("compact_asked")) >= 0 {
		t.Fatalf("baton compacted in the human's turn:\n%s", eventLog(s))
	}
	signal(t, dir, "human")
	waitSequence(t, s, 5*time.Minute,
		is("boundary_waits", "phase", "P1", "turn_by", "human"),
		kind("compact_asked"),
		is("compact_started", "by_baton", "true"),
		is("phase_started", "phase", "P1"),
		is("brief", "type", "boundary", "phase", "P1"),
		is("phase_done", "phase", "P1"),
		kind("plan_complete"),
	)
	if got := strings.TrimSpace(readFile(dir, "scratch/finished.txt")); got != "finished" {
		t.Errorf("the human's file holds %q: the turn was cut short", got)
	}
	if n := count(s.Events(), kind("boundary_waits")); n != 1 {
		t.Errorf("told %d times", n)
	}
	none(t, s, "stop_refused", "escalated", "compact_failed")
}

// changePlan: the phase agent's P1 waits in a command until the test lets it go, and the human changes
// P1's section meanwhile.
const changePlan = `# Change plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one
After: none

1. Run exactly this command with the Bash tool, with its timeout set to 600000 ms: sh wait.sh edited — it waits a while, then says so.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// The human changes P1's section while its phase agent is at work. The lead follows the change at its next
// stop, and baton passes the new text on to the agent with its next tool call; the agent does what the
// section says now, and its work lands with it. (On v0.8.1 the change never reached a live agent.)
func TestAPlanChangeReachesTheAgentAtWork(t *testing.T) {
	dir := projectWith(t, map[string]string{"wait.sh": startedWait})
	Attach(t, dir, changePlan)
	s := StartAgents(t, dir, []string{"BATON_FOREGROUND_MAX=10m"}, "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh wait.sh edited)", "Write")
	s.AutoApprove()
	waitStarted(t, s, dir, "edited", 5*time.Minute)
	if !s.Until("the lead to finish P0", 3*time.Minute, func() bool { return first(s.Events(), is("phase_done", "phase", "P0")) >= 0 }) {
		t.Fatalf("the lead never finished P0:\n%s", eventLog(s))
	}
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	old := "Write the single word one into a new file named one.txt. Commit it: git add one.txt, then"
	doc := strings.Replace(changePlan, old, "Write the single word one into a new file named one.txt, and the single word extra into a new file named extra.txt. "+
		"Commit both: git add one.txt extra.txt, then", 1)
	writeFile(t, dir, "plan.md", doc)
	s.Type("Reply with the single word ok.")
	waitSequence(t, s, 3*time.Minute,
		is("plan_changed", "adopted", "true"),
		is("plan_told", "phase", "P1", "rules", "false", "section", "true"),
	)
	signal(t, dir, "edited")
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	if first(s.WorkerEvents("P1"), is("message_delivered", "message", "plan")) < 0 {
		t.Errorf("the change was not given to the agent:\n%v", s.WorkerEvents("P1"))
	}
	if first(s.Events(), is("landed", "phase", "P1")) < 0 || first(s.Events(), is("plan_complete")) < 0 {
		t.Fatalf("P1 did not land:\n%s", eventLog(s))
	}
	if got := strings.TrimSpace(readFile(dir, "extra.txt")); got != "extra" {
		t.Errorf("extra.txt in the checkout: %q: the agent did not follow the change", got)
	}
	if n := count(s.Events(), is("plan_told")); n != 1 {
		t.Errorf("told %d times", n)
	}
	none(t, s, "controller_panic", "escalated")
}

// quietScript is a gate that waits, as a hung one would, until the test lets it end (releaseQuiet): its
// log stays still, and its shell sits in one open of a FIFO in the repository's git directory, so its
// process tree uses no CPU and starts no process (hold.sh's sleep starts one a second, which the stall
// alarm counts as work).
const quietScript = `#!/bin/sh
fifo="$(git rev-parse --git-common-dir)/quiet"
[ -p "$fifo" ] || mkfifo "$fifo"
echo "quiet: waiting"
read line <"$fifo"
echo "quiet: released"
`

// quietPlan's P0 hands baton quiet.sh as its gate, and goes on only once the test lets it end.
const quietPlan = `# Quiet plan

## P0 — Wait for the gate
After: none

1. Hand baton this gate, exactly: baton verify --notes 'the gate waits for the release' -- sh quiet.sh
2. End your turn. baton gives you the gate's result when it ends: do not run quiet.sh yourself, and do not wait for it or check on it.
3. When baton gives you the gate's result, report P0 done: baton done P0 --notes 'released'
`

// releaseQuiet lets the project's quiet.sh end, writing a line to the FIFO it waits on.
func releaseQuiet(t *testing.T, dir string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(filepath.Join(dir, ".git", "quiet"), os.O_WRONLY, 0) // waits for its reader
		if err == nil {
			_, err = f.WriteString("go\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("releasing quiet.sh: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("releasing quiet.sh: nothing reads its FIFO")
	}
}

// The lead waits on a gate whose log never grows and whose processes use no CPU, and nothing else in the
// run moves: once nothing has moved for stall_after (a minute here), baton tells the human once, naming
// what P0 waits on. Movement re-arms it: the gate ends, and the plan completes. (On v0.8.1 a run could
// stop for hours with no word. Since v0.8.6 a gate that works quietly, and an await, raise no alarm.)
func TestAStalledRunTellsTheHuman(t *testing.T) {
	dir := projectWith(t, map[string]string{"quiet.sh": quietScript})
	Attach(t, dir, quietPlan)
	s := StartEnv(t, dir, []string{"BATON_STALL_AFTER=1m"}, "--model", "haiku", "Start the attached plan, and follow baton's instructions.")
	s.Trust()
	// The lead goes idle on the gate: in place, or once it has reported P0 done while the gate runs.
	idle := func(e map[string]any) bool {
		return is("idle_on_job", "phase", "P0")(e) || is("phase_verifying", "phase", "P0")(e)
	}
	waitSequence(t, s, 3*time.Minute, is("job_queued", "phase", "P0"), idle)
	stalled := s.WaitEvent("run_stalled", 5*time.Minute)
	if stalled == nil {
		t.Fatalf("the run never said it stalled:\n%s", eventLog(s))
	}
	ev := s.Events()
	if d := eventTime(stalled).Sub(eventTime(ev[first(ev, idle)])); d < time.Minute {
		t.Errorf("stalled %s after the lead went idle, under a minute", d)
	}
	phases, _ := stalled["phases"].(map[string]any)
	if p0 := fmt.Sprint(phases["P0"]); !strings.Contains(p0, "log has not grown") {
		t.Errorf("run_stalled names P0 as %q", p0)
	}
	releaseQuiet(t, dir)
	s.Until("the plan to complete", 4*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	if first(s.Events(), is("phase_done", "phase", "P0")) < 0 {
		t.Fatalf("P0 was not done once the job ended:\n%s", eventLog(s))
	}
	if n := count(s.Events(), kind("run_stalled")); n != 1 {
		t.Errorf("said %d times", n)
	}
	none(t, s, "escalated")
}
