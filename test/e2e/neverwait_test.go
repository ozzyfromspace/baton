//go:build !windows

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// NewGateProject makes a project whose gate takes a while, as real ones do: gate.sh sleeps for seconds,
// then passes only if the file marker holds the single word ok, and says a word when it does. The gate
// writes its pid to .git/gate.pid.
// The project commits a .baton-lane that needs nothing set up, and records each call it gets (its
// subcommand) in .git/lane-calls, out of the working tree.
func NewGateProject(t *testing.T, seconds int) string {
	t.Helper()
	dir := NewProject(t)
	files := map[string]string{
		"gate.sh": fmt.Sprintf(`#!/bin/sh
# The project's gate: slow, and green only when marker holds the single word ok.
echo $$ >"${BATON_ROOT:-.}/.git/gate.pid"
echo "gate: checking"
sleep %d
if [ "$(cat marker 2>/dev/null)" = ok ]; then
	echo "gate: passed, and the word is heron"
	exit 0
fi
echo "gate: FAILED: the file marker must hold the single word ok, and it holds: $(cat marker 2>/dev/null || echo nothing)"
exit 1
`, seconds),
		".baton-lane": `#!/bin/sh
# Lanes need nothing set up here. Each call is recorded.
echo "$1" >>"$BATON_ROOT/.git/lane-calls"
case $1 in
slots) echo 1 ;;
run) cd "$2" && sh -c "$3" ;;
esac
`,
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		Git(t, dir, "add", name)
	}
	Git(t, dir, "commit", "-q", "-m", "chore: the gate, and its lanes")
	return dir
}

// laneCalls is what the project's .baton-lane was called for so far, in order.
func laneCalls(dir string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, ".git", "lane-calls"))
	return strings.Fields(string(b))
}

// gatePlan's P0 hands its gate to baton, and cannot be finished before the gate's result: it needs the
// word the gate says. P1 needs nothing, so the run can switch to it; P2 needs both.
const gatePlan = `# Gate plan

## P0 — Mark the work
After: none

1. Write the single word ok into a new file named marker, and commit it: git add marker, then git commit -m 'feat: the marker'.
2. Hand the project's gate to baton, exactly: baton verify ./gate.sh --notes 'marker committed; the gate runs, and its word goes into the file word'
3. End your turn. The gate takes a while, and baton gives you its result when it ends: do not run the gate yourself, and do not wait for it or check on it.
4. When the gate has passed, the last line of its output names a word. Write that word into a new file named word, commit it, and report P0 done.

P0 cannot be finished before baton gives you the gate's result, so do not report it done before then.

## P1 — Say hello
After: none

Run echo hello, then report P1 done.

## P2 — Say goodbye
After: P0, P1

Run echo goodbye, then report P2 done.
`

// is matches an event of kind whose fields hold the values given in pairs: is("phase_done", "phase", "P0").
func is(kind string, fields ...string) func(map[string]any) bool {
	return func(e map[string]any) bool {
		if e["kind"] != kind {
			return false
		}
		for i := 0; i+1 < len(fields); i += 2 {
			if fmt.Sprint(e[fields[i]]) != fields[i+1] {
				return false
			}
		}
		return true
	}
}

// first is the index of the first event cond matches, or -1.
func first(events []map[string]any, cond func(map[string]any) bool) int {
	for i, e := range events {
		if cond(e) {
			return i
		}
	}
	return -1
}

// none fails the test for every event of the kinds given.
func none(t *testing.T, s *Session, kinds ...string) {
	t.Helper()
	for _, e := range s.Events() {
		for _, k := range kinds {
			if e["kind"] == k {
				t.Errorf("unexpected %v", e)
			}
		}
	}
}

// The model hands P0's gate to baton and stops. The gate runs past switch_after, so baton sets P0 aside
// and starts P1, which needs nothing from P0. P2 needs both, so once P1 is done baton waits for the gate;
// when it passes, P0 resumes with its result and is done, and only then does P2 start. The lane is
// brought up, runs the gate, and comes down when the plan is complete.
func TestALongGateSwitchesToAnIndependentPhase(t *testing.T) {
	dir := NewGateProject(t, 45)
	Attach(t, dir, gatePlan)
	s := StartEnv(t, dir, []string{"BATON_SWITCH_AFTER=10s"}, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	waitSequence(t, s, 8*time.Minute,
		is("job_queued", "phase", "P0"),
		is("idle_on_job", "phase", "P0"),
		is("phase_parked", "phase", "P0", "reason", "job"),
		is("phase_switched", "from", "P0", "to", "P1"),
		is("brief", "type", "switch", "phase", "P1"),
		is("phase_done", "phase", "P1"),
		is("phase_resumed", "phase", "P0"),
		is("brief", "type", "resume", "phase", "P0"),
		is("phase_done", "phase", "P0"),
		is("phase_started", "phase", "P2"),
		kind("plan_complete"),
	)
	evs := s.Events()
	passed := first(evs, is("job_finished", "phase", "P0", "status", "passed"))
	if passed < 0 || passed > first(evs, is("phase_resumed", "phase", "P0")) {
		t.Errorf("P0 resumed before its gate passed:\n%s", eventLog(s))
	}
	none(t, s, "switch_cancelled", "escalated", "compact_failed")
	if got := strings.TrimSpace(Git(t, dir, "show", "HEAD:word")); got != "heron" {
		t.Errorf("word at HEAD: %q", got)
	}
	if !s.Until("the lane to come down", 30*time.Second, func() bool {
		calls := strings.Join(laneCalls(dir), " ")
		return strings.Contains(calls, "up run") && strings.HasSuffix(calls, "down")
	}) {
		t.Errorf("the lane script's calls: %v", laneCalls(dir))
	}
}

// redPlan's P0 commits a marker the gate turns down, and is reported done while the gate runs. P2 needs P0.
const redPlan = `# Red plan

## P0 — Mark the work
After: none

1. Write the single word draft into a new file named marker, and commit it: git add marker, then git commit -m 'feat: a first marker'. Write exactly draft: the gate decides what marker must hold, and says so if it fails.
2. Hand the project's gate to baton, exactly: baton verify ./gate.sh --notes 'marker committed as draft; the gate decides what it must hold'
3. Report P0 done right away, while the gate runs: baton marks P0 done once the gate passes. Do not run the gate yourself, and do not read it.

If baton brings P0 back because the gate failed, do what the gate's output says, commit, hand the gate to baton again as in step 2 (with notes saying what you fixed), and report P0 done again.

## P1 — Say hello
After: none

Run echo hello, then report P1 done.

## P2 — Say goodbye
After: P0

Run echo goodbye, then report P2 done.
`

// Two-stage done, red then green: P0 is reported done while its gate runs, and the run moves on to P1.
// The gate fails, so P0 comes back from its notes with the gate's output, is fixed, and is reported done
// again; this time the gate passes and baton marks P0 done. P2, which needs P0, starts only then.
func TestARedGateReopensThePhase(t *testing.T) {
	dir := NewGateProject(t, 30)
	Attach(t, dir, redPlan)
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	waitSequence(t, s, 10*time.Minute,
		is("phase_verifying", "phase", "P0"),
		is("phase_parked", "phase", "P0", "reason", "red"),
		is("phase_resumed", "phase", "P0"),
		is("brief", "type", "resume", "phase", "P0"),
		is("job_queued", "phase", "P0"),
		is("phase_verifying", "phase", "P0"),
		is("phase_done", "phase", "P0", "by", "verification"),
		is("phase_started", "phase", "P2"),
		kind("plan_complete"),
	)
	evs := s.Events()
	// P1 went on while P0's first gate ran, which may have failed before P1 even started.
	if i := first(evs, is("phase_started", "phase", "P1")); i < 0 || i < first(evs, is("phase_verifying", "phase", "P0")) || first(evs, is("phase_done", "phase", "P1")) < 0 {
		t.Errorf("P1 did not go on while P0 verified:\n%s", eventLog(s))
	}
	var verdicts []string
	for _, e := range evs {
		if e["kind"] == "job_finished" && e["phase"] == "P0" {
			verdicts = append(verdicts, fmt.Sprint(e["status"]))
		}
	}
	if strings.Join(verdicts, " ") != "failed passed" {
		t.Errorf("P0's gates: %v", verdicts)
	}
	if got := strings.TrimSpace(Git(t, dir, "show", "HEAD:marker")); got != "ok" {
		t.Errorf("marker at HEAD: %q", got)
	}
	none(t, s, "escalated", "compact_failed")
}

// A gate that ends before switch_after is given to the model where it is: nothing is set aside, and P0
// is done from its result.
func TestAQuickGateIsDeliveredInPlace(t *testing.T) {
	dir := NewGateProject(t, 10)
	Attach(t, dir, gatePlan)
	s := StartEnv(t, dir, []string{"BATON_SWITCH_AFTER=5m"}, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	waitSequence(t, s, 4*time.Minute,
		is("job_queued", "phase", "P0"),
		is("job_finished", "phase", "P0", "status", "passed"),
		kind("results_delivered"),
		is("phase_done", "phase", "P0"),
	)
	none(t, s, "phase_switched", "phase_parked", "switch_cancelled", "phase_resumed")
}

// slowPlan's P0 cannot be finished before its command ends: only the command's output says what to write.
const slowPlan = `# Slow plan

## P0 — Hear the slow script out
Run ./slow.sh with the Bash tool, as it is: in the foreground, not in the background, and with no timeout of your own. It takes about 40 seconds, and the last thing it prints is a word. Write that word into a new file named answer, commit it, and report P0 done.
Nothing else in P0 can be done before the script ends. If the command moves to the background, end your turn: its end wakes you. Whenever you are told to keep working, end your turn again.

## P1 — Say goodbye
Run echo goodbye.
`

// foreground_max: a long command, run in the foreground, moves to the background after a few seconds
// (CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS, which baton sets) rather than holding the session. The model's
// first stop on it is refused once, its next allowed, and the command's end wakes the model, which
// finishes P0 from its output.
func TestALongCommandMovesToTheBackground(t *testing.T) {
	dir := NewProject(t)
	os.WriteFile(filepath.Join(dir, "slow.sh"), []byte("#!/bin/sh\necho 'slow: working'\nsleep 40\necho 'slow: the word is pelican'\n"), 0o755)
	Git(t, dir, "add", "slow.sh")
	Git(t, dir, "commit", "-q", "-m", "chore: the slow script")
	Attach(t, dir, slowPlan)
	s := StartEnv(t, dir, []string{"BATON_FOREGROUND_MAX=5s"}, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	waitSequence(t, s, 5*time.Minute,
		is("stop_refused", "why", "background work"),
		is("idle_on_background", "phase", "P0"),
		is("turn_started", "by", "system"),
		is("phase_done", "phase", "P0"),
	)
	if b, _ := os.ReadFile(filepath.Join(dir, "answer")); !strings.Contains(string(b), "pelican") {
		t.Errorf("answer: %q", b)
	}
}

// Dropping the plan stops its running job: the gate's process ends, the job's log is removed, and the
// lane comes down.
func TestDropStopsARunningJob(t *testing.T) {
	dir := NewGateProject(t, 600)
	Attach(t, dir, gatePlan)
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	started := s.WaitEvent("job_started", 3*time.Minute)
	if started == nil || s.WaitEvent("idle_on_job", 2*time.Minute) == nil {
		t.Fatalf("events:\n%s", eventLog(s))
	}
	log, _ := started["log"].(string)
	if _, err := os.Stat(log); err != nil {
		t.Fatalf("the job's log: %v", err)
	}
	var gate int
	if !s.Until("the gate to start", 30*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, ".git", "gate.pid"))
		gate, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return gate > 0
	}) {
		t.Fatal("the gate never started")
	}
	s.WaitQuiet(3*time.Second, time.Minute)
	s.Type("Run exactly this one command with the Bash tool: baton drop\nThen reply DONE.")
	dropped := s.WaitEvent("dropped", 2*time.Minute)
	if dropped == nil {
		t.Fatalf("events:\n%s", eventLog(s))
	}
	if fmt.Sprint(dropped["stopped"]) != "[j1]" {
		t.Errorf("dropped: %v", dropped)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("the job's log is still there: %v", err)
	}
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	runner := int(started["pid"].(float64))
	if !s.Until("the gate and its runner to end", 30*time.Second, func() bool { return !alive(gate) && !alive(runner) }) {
		t.Errorf("still running: gate %d %v, runner %d %v", gate, alive(gate), runner, alive(runner))
	}
	if !s.Until("the lane to come down", 30*time.Second, func() bool {
		calls := laneCalls(dir)
		return len(calls) > 0 && calls[len(calls)-1] == "down"
	}) {
		t.Errorf("the lane script's calls: %v", laneCalls(dir))
	}
}
