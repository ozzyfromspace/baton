//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// proposeGreeting has the model propose writing a file, then follow baton. acceptEdits lets the Write
// tool through without a permission prompt, so the only dialog is baton's question (a test that pressed
// Enter on dialogs would pick its first option, Wait for me).
const proposeGreeting = `Before anything else, run exactly this one command with the Bash tool: ` +
	`baton propose "write the word hello into greeting.txt with the Write tool" --because "the plan does not say which greeting to use" --undo "remove greeting.txt" ` +
	`— then do exactly what baton tells you.`

func startProposal(t *testing.T, timeout string) (*Session, string) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=" + timeout}, "--model", "haiku", "--permission-mode", "acceptEdits", proposeGreeting)
	s.Trust()
	return s, dir
}

// Nobody answers: at the deadline baton picks Go ahead itself (it types 3 once its question is alone on
// screen), and the model does what it proposed.
func TestProposalGoesAheadWhenNobodyAnswers(t *testing.T) {
	s, dir := startProposal(t, "20s")
	waitSequence(t, s, 5*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "proposed" && e["id"] == "d1" },
		func(e map[string]any) bool { return e["kind"] == "dialog_open" && e["dialog"] == "proposal" },
		func(e map[string]any) bool { return e["kind"] == "proposal_asked" && e["id"] == "d1" },
		func(e map[string]any) bool {
			return e["kind"] == "auto_answered" && e["dialog"] == "proposal" && e["key"] == "3"
		},
		func(e map[string]any) bool {
			return e["kind"] == "proposal_resolved" && e["by"] == "timeout" && e["answer"] == "Go ahead"
		},
	)
	greeting := filepath.Join(dir, "greeting.txt")
	if !s.Until("greeting.txt", 2*time.Minute, func() bool { _, err := os.Stat(greeting); return err == nil }) {
		t.Fatalf("the model did not do what it proposed; events: %v", s.Events())
	}
}

// The human answers Wait for me: the run holds for them (no second question, nothing done), and their
// next message releases the hold.
func TestWaitForMeHoldsTheRun(t *testing.T) {
	s, dir := startProposal(t, "5m")
	waitSequence(t, s, 3*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "proposal_asked" && e["id"] == "d1" },
	)
	time.Sleep(2 * time.Second)
	s.pty.Write([]byte("1")) // the human picks Wait for me
	waitSequence(t, s, 2*time.Minute,
		func(e map[string]any) bool {
			return e["kind"] == "proposal_resolved" && e["answer"] == "Wait for me" && e["by"] == "held"
		},
		func(e map[string]any) bool { return e["kind"] == "escalated" && e["type"] == "decision" },
	)
	s.WaitQuiet(5*time.Second, time.Minute) // the model ends its turn
	if _, err := os.Stat(filepath.Join(dir, "greeting.txt")); err == nil {
		t.Fatal("the model did what it proposed while the human held it")
	}
	s.Type("Do not write greeting.txt. Just reply OK.")
	waitSequence(t, s, 2*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "human" },
		func(e map[string]any) bool { return e["kind"] == "hold_released" && e["id"] == "d1" },
	)
}
