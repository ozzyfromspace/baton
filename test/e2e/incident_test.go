//go:build !windows

package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// greeterPlan is the incident's plan in miniature: every phase ends committed, in a repository whose
// history is signed. Nothing in it says what to do when signing fails.
const greeterPlan = "# Greeter plan\n\n## Standing rules\n\n- Every phase ends with its work committed.\n\n" +
	"## P0 — Greeting\n\nCreate `greeting.txt` containing the single line `hello`. Verify it with `cat greeting.txt`. " +
	"Commit it with the message `feat: greeting`.\n\n" +
	"## P1 — Farewell\n\nCreate `farewell.txt` containing the single line `bye`. Verify it with `cat farewell.txt`. " +
	"Commit it with the message `feat: farewell`.\n\n" +
	"## Verification\n\n`git log --oneline` shows both commits.\n"

// The incident behind graduated escalation (docs/escalation.md), replayed as S2 did: Sonnet in auto mode,
// nobody at the terminal, and gpg timing out on every commit after the plan starts. v0.1.1 hard-blocked
// here with the work staged, every time. Now the run must work around it without the human: propose (or
// note) committing unsigned with a way to re-sign later, go ahead when nobody answers, and finish the
// plan with every phase committed.
func TestSigningOutageDoesNotStopTheRun(t *testing.T) {
	if os.Getenv("BATON_E2E_SONNET") != "1" {
		t.Skip("set BATON_E2E_SONNET=1 to replay the signing outage on Sonnet")
	}
	dir, breakSigning := NewSignedProject(t)
	Attach(t, dir, greeterPlan)
	breakSigning()
	s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=40s"}, "--model", "sonnet", "--permission-mode", "auto", "Start the attached plan.")
	s.Trust()
	s.Until("the plan to complete, or a hard stop", 20*time.Minute, func() bool {
		for _, e := range s.Events() {
			if e["kind"] == "plan_complete" || e["kind"] == "escalated" {
				return true
			}
		}
		return false
	})
	var complete, worked bool
	for _, e := range s.Events() {
		switch e["kind"] {
		case "plan_complete":
			complete = true
		case "proposed", "note":
			worked = true
		case "escalated":
			t.Errorf("the run stopped for the human: %v", e)
		}
	}
	if !complete || !worked {
		t.Fatalf("plan complete: %v, a decision made without the human: %v; events:\n%s", complete, worked, eventLog(s))
	}
	if got := Git(t, dir, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("work left uncommitted:\n%s", got)
	}
	log := Git(t, dir, "log", "--format=%s")
	for _, want := range []string{"feat: greeting", "feat: farewell"} {
		if !strings.Contains(log, want) {
			t.Errorf("no %q commit in:\n%s", want, log)
		}
	}
	for _, f := range []string{"greeting.txt", "farewell.txt"} {
		if Git(t, dir, "ls-files", f) == "" {
			t.Errorf("%s was never committed", f)
		}
	}
}
