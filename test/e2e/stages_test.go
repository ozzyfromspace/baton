//go:build !windows

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// NewStagedProject makes a project whose gate is a script of three `baton stage` steps, as the skill asks
// a long gate to be built. Stage two checks that the file marker holds the single word ok, and prints a
// go test failure line when it does not; a gate that failed then gathers its logs for ten minutes, which
// is what a model should not wait for. The project commits a .baton-lane that needs nothing set up.
func NewStagedProject(t *testing.T) string {
	t.Helper()
	dir := NewProject(t)
	files := map[string]string{
		"gate.sh": `#!/bin/sh
# The project's gate: three stages. Stage two checks marker; a failed gate then gathers its logs.
status=0
baton stage one -- sh -c 'echo "one: fine"' || status=1
baton stage two --runner go -- sh -c 'if [ "$(cat marker 2>/dev/null)" = ok ]; then echo "--- PASS: TestMarker (0.00s)"; exit 0; fi; echo "--- FAIL: TestMarker (0.00s) the file marker must hold the single word ok, not $(cat marker 2>/dev/null || echo nothing)"; exit 1' || status=1
baton stage three -- sh -c 'echo "three: fine"' || status=1
if [ "$status" -ne 0 ]; then
	echo "gate: gathering the logs of the failure, which takes ten minutes"
	sleep 600
fi
exit "$status"
`,
		".baton-lane": `#!/bin/sh
# Lanes need nothing set up here.
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
	Git(t, dir, "commit", "-q", "-m", "chore: the staged gate, and its lanes")
	return dir
}

// stagedPlan's P0 commits a marker the gate's second stage turns down, and acts on the failure as soon as
// baton says the gate is failing, while the gate still runs.
const stagedPlan = `# Staged gate plan

## P0 — Mark the work
After: none

1. Write the single word draft into a new file named marker, and commit it: git add marker, then git commit -m 'feat: a first marker'. Write exactly draft: the gate decides what marker must hold.
2. Hand the project's gate to baton, exactly: baton verify ./gate.sh --notes 'marker committed as draft; the gate decides what it must hold'
3. End your turn. Do not run the gate yourself, do not read its log, and do not report P0 done yet.
4. When baton tells you the gate is failing as it runs, act at once, in this order:
   - stop the gate with the baton stop command baton's message names;
   - write the single word the failure line asks for into marker, and commit it: git add marker, then git commit -m 'fix: the marker';
   - rerun only what failed, exactly: baton verify --failed --notes 'marker fixed; rerunning what failed'
   - report P0 done right away: baton marks it done once that rerun passes.
`

// A staged gate's failure is known as it is printed, and its rerun runs only what failed. Stage two of
// P0's gate fails, stage three is skipped, and the gate runs on gathering logs; the first-failure look-in
// reaches the model, which stops the gate, fixes the marker and reruns with --failed. The rerun carries
// stage one, runs two and three, and passes, which finishes P0.
func TestAStagedGateIsStoppedAtItsFirstFailureAndRerunOnWhatFailed(t *testing.T) {
	dir := NewStagedProject(t)
	Attach(t, dir, stagedPlan)
	s := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.",
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(printf:*)", "Bash(echo:*)", "Write", "Edit")
	s.Trust()
	waitSequence(t, s, 12*time.Minute,
		is("job_queued", "phase", "P0"),
		is("job_failing", "phase", "P0", "stage", "two"),
		is("job_stopped", "phase", "P0"),
		func(e map[string]any) bool { return e["kind"] == "job_queued" && e["reruns"] != nil },
		is("job_finished", "phase", "P0", "status", "passed"),
		is("phase_done", "phase", "P0"),
		kind("plan_complete"),
	)
	evs := s.Events()
	gate := evs[first(evs, is("job_queued", "phase", "P0"))]["job"]
	rerun := evs[first(evs, func(e map[string]any) bool { return e["kind"] == "job_queued" && e["reruns"] != nil })]
	if rerun["reruns"] != gate {
		t.Errorf("the rerun reruns %v, not the gate %v", rerun["reruns"], gate)
	}
	stopped := first(evs, is("job_stopped", "job", fmt.Sprint(gate)))
	if stopped < 0 {
		t.Errorf("the gate %v was not the job stopped:\n%s", gate, eventLog(s))
	}
	told := first(evs, func(e map[string]any) bool {
		if e["kind"] == "look_in_told" {
			return e["type"] == "first_failure" && e["job"] == gate
		}
		kinds, _ := e["kinds"].([]any)
		return e["kind"] == "look_in_typed" && slices.Contains(kinds, any("first_failure"))
	})
	if told < 0 || told > stopped {
		t.Errorf("the first-failure look-in did not reach the model before the stop:\n%s", eventLog(s))
	}
	if got, want := stagesOf(evs, gate), "one passed, two failed, three skipped"; got != want {
		t.Errorf("the gate's stages: %s, not %s", got, want)
	}
	if got, want := stagesOf(evs, rerun["job"]), "one carried, two passed, three passed"; got != want {
		t.Errorf("the rerun's stages: %s, not %s", got, want)
	}
	for _, e := range evs {
		if e["kind"] == "refused" && e["command"] == "done" {
			t.Errorf("done refused: %v", e)
		}
	}
	if got := strings.TrimSpace(Git(t, dir, "show", "HEAD:marker")); got != "ok" {
		t.Errorf("marker at HEAD: %q", got)
	}
	none(t, s, "escalated", "compact_failed")
}

// stagesOf is how each stage of job ended, in order: "one passed, two failed".
func stagesOf(evs []map[string]any, job any) string {
	var out []string
	for _, e := range evs {
		if e["kind"] == "stage_finished" && e["job"] == job {
			out = append(out, fmt.Sprintf("%v %v", e["stage"], e["status"]))
		}
	}
	return strings.Join(out, ", ")
}
