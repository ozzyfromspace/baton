//go:build !windows

package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// handPlan is raisePlan's phase, whose gate fails the first time it runs, on haiku/low, beside a phase a
// phase agent works on for four minutes. The lead's first turn lasts long enough for that agent to be at
// work when it ends, as its Stop reports, so that no restart of the lead can come once P0 is raised.
const handPlan = `# Hand-over

## P0 — Gate
After: none
Model: haiku
Effort: low

First run this with the Bash tool, in the foreground, setting its timeout to 120000 milliseconds: sleep 45
Then commit nothing new, and run this gate with baton: baton verify "sh gate.sh" --notes 'checking'
Then end your turn: baton wakes you with its result.
When its result is red, run the same gate again the same way, with: --why 'the gate is flaky', and end your turn again.
When it passes, report P0 done: baton done P0 --notes 'the gate passed'

## P1 — Beside
After: none
Model: haiku
Effort: low

Run this with the Bash tool, in the foreground, setting its timeout to 300000 milliseconds: sleep 240
It takes four minutes. Then report P1 done: baton done P1 --notes 'slept'
`

// A phase raised while a phase agent is at work goes to a phase agent on its raised tier: the lead cannot
// restart on it without ending the agent at work, so baton sets the phase aside for one (phase_handed) and
// spawns it on haiku/medium, which its replies are served, rather than waiting for a restart that would not
// come and giving up (tier_given_up). The plan completes.
func TestARaisedPhaseGoesToAnAgentWhileAnotherWorks(t *testing.T) {
	dir := NewProject(t)
	count := filepath.Join(t.TempDir(), "runs")
	writeFile(t, dir, "gate.sh", fmt.Sprintf("n=$(cat %q 2>/dev/null || echo 0); n=$((n+1)); echo $n >%q\n"+
		"if [ \"$n\" -lt 2 ]; then sleep 20; echo 'FAIL: TestGate (run '$n')'; exit 1; fi\necho ok\n", count, count))
	Git(t, dir, "add", "gate.sh")
	Git(t, dir, "commit", "-q", "-m", "test: the gate")
	Attach(t, dir, handPlan)
	s := StartAgents(t, dir, []string{"BATON_WORKERS=2", "BATON_ESCALATE_AFTER=1"}, "--effort", "low")
	s.ApprovePermissions()
	if !s.Until("the plan to complete", 12*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 }) {
		t.Fatal("the plan did not complete")
	}
	ev := s.Events()
	raised := at(ev, is("tier_raised", "phase", "P0", "from", "haiku/low", "to", "haiku/medium"))
	handed := at(ev, is("phase_handed", "phase", "P0", "tier", "haiku/medium"))
	spawned := at(ev, is("worker_spawned", "phase", "P0", "tier", "haiku/medium"))
	if raised.IsZero() || handed.IsZero() || spawned.IsZero() || handed.Before(raised) || spawned.Before(handed) {
		logEvents(t, "the run", ev)
		t.Fatalf("raised %v, handed %v, spawned %v", raised, handed, spawned)
	}
	if first(ev, is("worker_spawned", "phase", "P1", "tier", "haiku/low")) < 0 {
		t.Errorf("P1 did not go to an agent on its tier")
	}
	for _, kind := range []string{"tier_given_up", "tier_restart"} {
		if first(ev, is(kind)) >= 0 {
			logEvents(t, "the run", ev)
			t.Errorf("%s: the lead waited for a restart, or restarted with an agent at work", kind)
		}
	}
	if first(s.WorkerEvents("P0"), is("tier_served", "asked", "haiku/medium", "fits", "true")) < 0 {
		t.Errorf("P0's agent was not served haiku/medium")
	}
}
