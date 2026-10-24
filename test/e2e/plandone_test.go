//go:build !windows

package e2e

import (
	"testing"
	"time"
)

// firstPlan is the session's first plan: one phase, which the lead does.
const firstPlan = `# First plan

## P0 — Say hello
After: none

Run echo hello, then report P0 done: baton done P0 --notes 'said hello'
`

// secondPlan is attached after firstPlan completes. Its last phase, a QA phase, goes to a phase agent
// (a run with agents passes a QA phase to one), so the agent's landing ends the plan while the lead holds
// no phase. Its first phase is the lead's: the first phase of a plan attached in a session is the lead's.
const secondPlan = `# Second plan

## P1 — Say zero
After: none

Run echo zero, then report P1 done: baton done P1 --notes 'said zero'

## P2 — Write two
Type: qa
After: P1

This phase has no checks to run. Write the single word two into a new file named two.txt. Commit it: git add two.txt, then git commit -m 'feat: two'. Then report P2 done: baton done P2 --notes 'wrote two'
`

// A second plan in one session is announced complete as the first was. In a real run, a later plan of a
// session finished when its last phase agent's work landed, and baton logged no plan_complete, pushed
// nothing and said nothing, since the first plan's end, told, stayed on record. Here the first plan ends
// by the lead's own done; the second is attached in the same session after it, and ends as its agent's
// work lands: that end is logged and announced, and the lead, idle, is woken to sum the run up.
func TestASecondPlanIsAnnouncedComplete(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, firstPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	if s.WaitEvent("plan_complete", 4*time.Minute) == nil {
		t.Fatalf("the first plan did not complete:\n%s", eventLog(s))
	}
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	writeFile(t, dir, "plan2.md", secondPlan)
	Git(t, dir, "add", "plan2.md")
	Git(t, dir, "commit", "-q", "-m", "docs: the second plan")
	s.Type("Run exactly this one command with the Bash tool: baton attach plan2.md --suggested\nThen do exactly what it prints.")

	s.Until("the second plan to complete", 10*time.Minute, func() bool {
		return count(s.Events(), is("plan_complete")) >= 2
	})
	ev := s.Events()
	if n := count(ev, is("plan_complete")); n != 2 {
		t.Fatalf("plan_complete logged %d times, want once for each plan:\n%s", n, eventLog(s))
	}
	attached := first(ev, func(e map[string]any) bool { return e["kind"] == "attached" && e["phases"] == 2.0 })
	landed := first(ev, is("landed", "phase", "P2"))
	if attached < 0 || first(ev, is("worker_spawned", "phase", "P2")) < attached || landed < attached {
		t.Fatalf("the second plan's P2 did not land from a phase agent (attached at %d, landed at %d):\n%s", attached, landed, eventLog(s))
	}
	var ends []int
	for i, e := range ev {
		if is("plan_complete")(e) {
			ends = append(ends, i)
		}
	}
	if ends[0] > attached || ends[1] < landed || ev[ends[1]]["by"] != "landed" {
		t.Errorf("plan_complete at %v, the second plan attached at %d and P2 landed at %d; the second by %v", ends, attached, landed, ev[ends[1]]["by"])
	}
	s.Until("the lead to be woken", 3*time.Minute, func() bool {
		ev := s.Events()
		_, j := eventAfter(ev, first(ev, is("landed", "phase", "P2")), "turn_started", func(e map[string]any) bool { return e["by"] == "baton" })
		return j >= 0
	})
	ev = s.Events()
	landed = first(ev, is("landed", "phase", "P2"))
	if _, j := eventAfter(ev, landed, "turn_started", func(e map[string]any) bool { return e["by"] == "baton" }); j < 0 {
		t.Fatalf("the lead was not woken after the last landing:\n%s", eventLog(s))
	}
	if _, j := eventAfter(ev, landed, "mail_typed", is("mail_typed", "messages", "[done]")); j < 0 {
		t.Errorf("baton's message that the plan is complete was not typed to the lead after the landing")
	}
	none(t, s, "serial", "controller_panic", "land_returned")
}
