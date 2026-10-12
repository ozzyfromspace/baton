//go:build !windows

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Recovering a run (docs/research/recovery.md): a rewind, a crashed claude, a /clear mid-phase, and a gate
// that needs the checkout. No scenario provokes a refusal: those are tested on transcript fixtures.

// projectWith makes a project with the files given (scripts are executable), committed, and scratch/
// ignored: a scenario's model writes there what no phase commits.
func projectWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := NewProject(t)
	files[".gitignore"] = "scratch/\n"
	for name, text := range files {
		writeFile(t, dir, name, text)
		Git(t, dir, "add", name)
	}
	Git(t, dir, "commit", "-q", "-m", "chore: the scripts")
	return dir
}

// waitPlan's P0 waits on hold.sh, which baton runs, and goes on only once the test releases it: until then
// its session idles, and does what the human asks meanwhile.
const waitPlan = `# Wait plan

## P0 — Wait for the release
After: none

1. Hand baton this wait, exactly: baton await 'sh hold.sh' --notes 'waiting for the release'
2. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it. Meanwhile, do what the human asks, and end your turn when that is done.
3. When baton gives you the wait's result, report P0 done: baton done P0 --notes 'released'
`

// count is how many events cond matches.
func count(events []map[string]any, cond func(map[string]any) bool) int {
	return len(filter(events, cond))
}

// screenFrom is what the screen showed after the first from bytes of it (Screen), for a wait on what a
// key brought up.
func (s *Session) screenFrom(from int) string {
	sc := s.Screen()
	if from > len(sc) {
		return ""
	}
	return sc[from:]
}

// Rewind rewinds the conversation as a person does: Esc Esc opens the menu, with the current input last
// and selected; ups moves up to the prompt to go back to before; Enter asks how, and option answers (for a
// prompt whose turn changed code, 1 restores code and conversation, 2 the conversation alone). The rewind
// puts the prompt back in the input box, which it empties again.
func (s *Session) Rewind(ups int, option string) bool {
	from := len(s.Screen())
	s.pty.Write([]byte("\x1b"))
	time.Sleep(300 * time.Millisecond)
	s.pty.Write([]byte("\x1b"))
	if !s.Until("the rewind menu", 15*time.Second, func() bool { return strings.Contains(s.screenFrom(from), "Entertocontinue") }) {
		return false
	}
	time.Sleep(time.Second)
	for i := 0; i < ups; i++ {
		s.Key("\x1b[A")
	}
	from = len(s.Screen())
	s.Key("\r")
	if !s.Until("the rewind's options", 15*time.Second, func() bool { return strings.Contains(s.screenFrom(from), "Restoreconversation") }) {
		return false
	}
	time.Sleep(500 * time.Millisecond)
	s.Key(option)
	time.Sleep(2500 * time.Millisecond)
	s.Key("\x15")
	s.Key("\x15")
	return true
}

// The human rewinds the conversation past a turn that wrote a file and ran baton note, and goes on with
// another prompt. No hook fires during the rewind: baton notices it at the next prompt's first tool call,
// and tells the model, in that tool's result, that the note stands and the file is as it is on disk. Its
// record is not rolled back, and the phase then finishes.
func TestARewindIsNoticedAndTheModelTold(t *testing.T) {
	dir := projectWith(t, map[string]string{"hold.sh": holdScript})
	Attach(t, dir, waitPlan)
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.",
		"--allowedTools", "Write", "Bash(echo:*)")
	s.Trust()
	s.AutoApprove()
	if s.WaitEvent("idle_on_job", 3*time.Minute) == nil {
		t.Fatalf("P0 never waited on its job:\n%s", eventLog(s))
	}
	s.WaitQuiet(3*time.Second, 30*time.Second)

	s.Type("Use the Write tool to create the file scratch/color.txt holding the single word blue. " +
		`Then run exactly this command with the Bash tool: baton note "chose blue for the color". Then end your turn.`)
	note := s.WaitEvent("note", 2*time.Minute)
	if note == nil {
		t.Fatalf("no note:\n%s", eventLog(s))
	}
	if !s.Until("the turn to end", 2*time.Minute, func() bool { return count(s.Events(), is("idle_on_job", "phase", "P0")) >= 2 }) {
		t.Fatalf("the turn that noted did not end:\n%s", eventLog(s))
	}
	s.WaitQuiet(3*time.Second, 30*time.Second)

	// Back to before the turn that noted, the conversation alone: the file stays.
	if !s.Rewind(1, "2") {
		t.Fatal("could not rewind")
	}
	s.Type("Run exactly this command with the Bash tool: echo four. Then end your turn.")
	rewound := s.WaitEvent("rewound", 2*time.Minute)
	if rewound == nil {
		t.Fatalf("the rewind went unnoticed:\n%s", eventLog(s))
	}
	if rewound["told"] != true || rewound["phase"] != "P0" || !slices.Contains(strs(rewound["decisions"]), note["id"].(string)) ||
		!slices.Contains(strs(rewound["files"]), "scratch/color.txt") {
		t.Errorf("rewound: %v (the note was %v)", rewound, note["id"])
	}
	if !s.Until("the model to be told", 2*time.Minute, func() bool {
		evs := s.Events()
		return before(evs, is("rewound"), is("message_delivered", "message", "rewind"))
	}) {
		t.Fatalf("the model was never told:\n%s", eventLog(s))
	}
	if got := readFile(dir, "scratch/color.txt"); strings.TrimSpace(got) != "blue" {
		t.Errorf("scratch/color.txt after the rewind: %q", got)
	}

	release(t, dir)
	s.Until("the plan to complete", 4*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if first(ev, is("phase_done", "phase", "P0")) < 0 {
		t.Fatalf("P0 did not finish:\n%s", eventLog(s))
	}
	if n := count(ev, is("rewound")); n != 1 {
		t.Errorf("%d rewinds noticed, want 1", n)
	}
	none(t, s, "escalated")
}

// strs is a JSON array of strings.
func strs(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

// claudeOf is the claude the baton process pid hosts: its pid and command line (0 and "" if none).
func claudeOf(t *testing.T, pid int) (int, string) {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-A", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || filepath.Base(f[2]) != "claude" {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid == pid {
			child, _ := strconv.Atoi(f[0])
			return child, strings.Join(f[2:], " ")
		}
	}
	return 0, ""
}

// crashPlan's P0 runs slow.sh, which takes half a minute and records each run of it.
const crashPlan = `# Crash plan

## P0 — Take a while
After: none

1. Run exactly this command with the Bash tool: sh slow.sh — it takes about half a minute, and says when it is done.
2. Then report P0 done: baton done P0 --notes 'slow.sh ran'
`

const slowScript = `#!/bin/sh
# Takes half a minute. Each run is recorded in the repository's git directory.
common=$(git rev-parse --git-common-dir)
echo $$ >>"$common/slow-runs"
echo "slow: working"
sleep 30
echo "slow: done"
`

// claude is killed in the middle of a turn. baton, the same process, brings it back: it resumes the
// conversation with the human's own flags, permission rules included, and tells the model the turn was
// lost. The phase then finishes.
func TestACrashedLeadComesBack(t *testing.T) {
	dir := projectWith(t, map[string]string{"slow.sh": slowScript})
	Attach(t, dir, crashPlan)
	// The rule names the command: auto mode drops a rule that allows a shell with any script.
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.", "--allowedTools", "Bash(sh slow.sh)")
	s.Trust()
	s.AutoApprove()
	if !s.Until("slow.sh to start", 3*time.Minute, func() bool { return readFile(filepath.Join(dir, ".git"), "slow-runs") != "" }) {
		t.Fatalf("slow.sh never ran:\n%s", eventLog(s))
	}
	pid, _ := claudeOf(t, s.cmd.Process.Pid)
	if pid == 0 {
		t.Fatal("no claude under baton")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	waitSequence(t, s, 5*time.Minute,
		is("lead_exited", "code", "137", "then", "resume"),
		kind("host_started"),
		is("lead_resumed", "phase", "P0", "woken", "true"),
		is("phase_done", "phase", "P0"),
		kind("plan_complete"),
	)
	ev := s.Events()
	// The session's SessionStart hooks run side by side: its start and the wake can be logged either way round.
	if !before(ev, is("lead_exited"), is("session_start", "source", "resume")) {
		t.Errorf("the conversation was not resumed:\n%s", eventLog(s))
	}
	for _, e := range filter(ev, is("host_started")) {
		if pid, _ := e["pid"].(float64); int(pid) != s.cmd.Process.Pid {
			t.Errorf("a host in another process: %v (baton's pid %d)", e, s.cmd.Process.Pid)
		}
	}
	if n := count(ev, is("lead_exited")); n != 1 {
		t.Errorf("%d exits, want 1:\n%s", n, eventLog(s))
	}
	_, args := claudeOf(t, s.cmd.Process.Pid)
	if !strings.Contains(args, "--allowedTools Bash(sh slow.sh)") || !strings.Contains(args, "--model haiku") {
		t.Errorf("the claude brought back runs without the human's flags: %s", args)
	}
	none(t, s, "escalated", "lead_fresh")
}

// The human runs /clear while the phase waits on its job. The fresh conversation is given the phase's
// brief, so it knows the phase it is in without being told, and finishes it once the job's result comes.
func TestAClearMidPhaseGetsThePhasesBrief(t *testing.T) {
	dir := projectWith(t, map[string]string{"hold.sh": holdScript})
	Attach(t, dir, waitPlan)
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	s.AutoApprove()
	if s.WaitEvent("idle_on_job", 3*time.Minute) == nil {
		t.Fatalf("P0 never waited on its job:\n%s", eventLog(s))
	}
	s.WaitQuiet(3*time.Second, 30*time.Second)

	s.Type("/clear")
	waitSequence(t, s, time.Minute,
		is("session_start", "source", "clear"),
		is("brief", "type", "fresh", "phase", "P0"),
	)
	s.WaitQuiet(3*time.Second, 30*time.Second)
	s.Type(`Which phase of the plan are you working on? Run exactly this command with the Bash tool, with its id and title in place of the dots: baton note "working on ..." Then end your turn.`)
	note := s.WaitEvent("note", 2*time.Minute)
	if what, _ := note["what"].(string); !strings.Contains(what, "P0") {
		t.Errorf("the fresh conversation does not know its phase: %v", note)
	}
	release(t, dir)
	s.Until("the plan to complete", 4*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if !before(ev, is("brief", "type", "fresh"), is("phase_done", "phase", "P0")) {
		t.Fatalf("P0 did not finish after the /clear:\n%s", eventLog(s))
	}
	none(t, s, "escalated")
}

// herePlan's P0 has a gate that needs the checkout, and P1, a worker's, waits for that gate to start
// before it finishes, so that its work is ready to land while the gate runs.
const herePlan = `# Here plan

## P0 — Check the checkout
After: none

1. Write the single word ok into a new file named marker, and commit it: git add marker, then git commit -m 'feat: the marker'.
2. Hand the gate to baton, to run in this checkout, exactly: baton verify --here 'sh gate.sh' --notes 'marker committed; the gate runs in the checkout'
3. End your turn. The gate takes a while, and baton gives you its result when it ends: do not run the gate yourself, and do not wait for it or check on it.
4. When the gate has passed, report P0 done: baton done P0 --notes 'the gate passed'

## P1 — Write one
After: none

1. Run exactly this command with the Bash tool: sh after-gate.sh — it waits until the lead session's gate has started.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
3. Report P1 done: baton done P1 --notes 'wrote one'
`

const (
	// hereGate says where it runs, then waits until the test releases it.
	hereGate = `#!/bin/sh
# The gate needs this checkout. It says where it runs, then waits until the test releases it.
common=$(git rev-parse --git-common-dir)
pwd -P >"$common/gate-pwd"
echo "gate: running in $(pwd -P)"
i=0
while [ ! -e "$common/go" ] && [ "$i" -lt 900 ]; do
	sleep 1
	i=$((i + 1))
done
echo "gate: passed"
`
	// afterGate waits, in a phase agent's worktree, until the lead's gate has started.
	afterGate = `#!/bin/sh
# Waits until the lead session's gate has started: it writes gate-pwd into the repository's git directory.
common=$(git rev-parse --git-common-dir)
i=0
while [ ! -e "$common/gate-pwd" ] && [ "$i" -lt 600 ]; do
	sleep 1
	i=$((i + 1))
done
echo "after-gate: the gate has started"
`
)

// The lead's gate runs in its checkout, with no lane (verify --here). A phase agent's work, ready to land
// meanwhile, waits for the gate, since landing it would change the files the gate reads: it lands once the
// gate has ended. The gate's result reaches the lead, which finishes its phase.
func TestAGateHereHoldsTheLanding(t *testing.T) {
	dir := projectWith(t, map[string]string{"gate.sh": hereGate, "after-gate.sh": afterGate})
	Attach(t, dir, herePlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(sh after-gate.sh)", "Write")
	s.AutoApprove()

	queued := s.WaitEvent("job_queued", 4*time.Minute)
	if queued == nil || queued["phase"] != "P0" || queued["here"] != true {
		t.Fatalf("P0's gate was not queued to run here: %v", queued)
	}
	job, _ := queued["job"].(string)
	deferred := s.WaitEvent("land_deferred", 8*time.Minute)
	if deferred == nil || deferred["phase"] != "P1" || deferred["why"] != "a gate runs in the checkout (job "+job+")" {
		t.Fatalf("P1's landing did not wait for the gate: %v", deferred)
	}
	if first(s.Events(), is("landed")) >= 0 || strings.Contains(Git(t, dir, "log", "--format=%s"), "feat: one") {
		t.Fatal("P1 landed while the gate ran")
	}
	ran, _ := filepath.EvalSymlinks(strings.TrimSpace(readFile(filepath.Join(dir, ".git"), "gate-pwd")))
	if checkout, _ := filepath.EvalSymlinks(dir); ran != checkout {
		t.Errorf("the gate ran in %q, not the checkout %q", ran, checkout)
	}

	release(t, dir)
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	started := first(ev, is("job_started", "job", job))
	finished := first(ev, is("job_finished", "job", job, "status", "passed"))
	landed := first(ev, is("landed", "phase", "P1"))
	switch {
	case started < 0 || ev[started]["here"] != true:
		t.Errorf("the gate did not start here: %v", ev[max(started, 0)])
	case finished < 0 || landed < 0:
		t.Fatalf("the gate did not pass, or P1 did not land:\n%s", eventLog(s))
	case !(first(ev, is("land_deferred", "phase", "P1")) < finished && finished < landed):
		t.Errorf("P1 landed (event %d) before the gate ended (event %d)", landed, finished)
	}
	if !before(ev, is("job_finished", "job", job), is("phase_done", "phase", "P0")) {
		t.Errorf("P0 was not done after its gate:\n%s", eventLog(s))
	}
	if got := readFile(dir, "one.txt"); strings.TrimSpace(got) != "one" {
		t.Errorf("one.txt in the checkout: %q", got)
	}
	none(t, s, "escalated", "land_returned", "controller_panic", "edit_refused")
}
