//go:build !windows

package e2e

import (
	"fmt"
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

func startProposal(t *testing.T, timeout string, args ...string) (*Session, string) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=" + timeout}, append(args, "--model", "haiku", "--permission-mode", "acceptEdits", proposeGreeting)...)
	s.Trust()
	return s, dir
}

// Nobody answers: at the deadline baton picks Go ahead itself, and the model does what it proposed. Where
// baton's mod runs, the mod answers the question itself, never sooner than a minute after it was shown,
// and no key is typed into any dialog; where it does not, the host types 3 once its question is alone on
// screen.
func TestProposalGoesAheadWhenNobodyAnswers(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  bool
	}{{"the mod settles it", true}, {"with no mod, the host types its answer", false}} {
		t.Run(c.name, func(t *testing.T) {
			var args []string
			steps := []func(map[string]any) bool{
				func(e map[string]any) bool { return e["kind"] == "proposed" && e["id"] == "d1" },
				func(e map[string]any) bool { return e["kind"] == "dialog_open" && e["dialog"] == "proposal" },
				func(e map[string]any) bool { return e["kind"] == "proposal_asked" && e["id"] == "d1" },
			}
			if !c.mod {
				args = []string{"--plugin-dir", noModPlugin(t)}
				steps = append(steps, func(e map[string]any) bool {
					return e["kind"] == "auto_answered" && e["dialog"] == "proposal" && e["key"] == "3"
				})
			}
			// Where the mod settles it, baton records the answer before the mod's report of it (mod_deadline),
			// so that a failure in between has the mod send it again.
			steps = append(steps, func(e map[string]any) bool {
				return e["kind"] == "proposal_resolved" && e["by"] == "timeout" && e["answer"] == "Go ahead"
			})
			s, dir := startProposal(t, "20s", args...)
			waitSequence(t, s, 5*time.Minute, steps...)
			if c.mod && !s.Until("the mod's report", time.Minute, func() bool { return first(s.Events(), is("mod_deadline", "answer", "Go ahead")) >= 0 }) {
				t.Errorf("the mod did not report the deadline:\n%s", eventLog(s))
			}
			greeting := filepath.Join(dir, "greeting.txt")
			if !s.Until("greeting.txt", 2*time.Minute, func() bool { _, err := os.Stat(greeting); return err == nil }) {
				t.Fatalf("the model did not do what it proposed; events: %v", s.Events())
			}
			ev := s.Events()
			if !c.mod {
				return
			}
			resolved := first(ev, is("proposal_resolved", "by", "timeout", "via", "mod"))
			if asked := first(ev, is("proposal_asked")); resolved < 0 || eventTime(ev[resolved]).Sub(eventTime(ev[asked])) < time.Minute {
				t.Errorf("not settled by the mod, a minute or more after it was asked:\n%s", eventLog(s))
			}
			none(t, s, "auto_answered")
		})
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

// Nobody answers, baton goes ahead, and the plan runs to its end. The human comes back, clears the
// session, and asks for whatever was done without them to be undone: baton hands the model the decision
// with its undo, and the model undoes it (the S3 spike, end to end). A plain folder, since the plan's
// files are never committed.
func TestALateUndoFindsWhatWentAhead(t *testing.T) {
	dir := NewPlainProject(t)
	Attach(t, dir, twoPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=20s"}, "--allowedTools", "Bash(echo:*),Bash(rm:*)",
		"--model", "haiku", "--permission-mode", "acceptEdits", proposeGreeting)
	s.Trust()
	waitSequence(t, s, 10*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "proposal_resolved" && e["by"] == "timeout" },
		func(e map[string]any) bool { return e["kind"] == "plan_complete" && e["decisions"] == 1.0 },
	)
	greeting := filepath.Join(dir, "greeting.txt")
	if _, err := os.Stat(greeting); err != nil {
		t.Fatalf("the model did not do what it proposed; events: %v", s.Events())
	}
	s.WaitQuiet(5*time.Second, time.Minute)
	s.Type("/clear")
	waitSequence(t, s, time.Minute, func(e map[string]any) bool { return e["kind"] == "session_start" && e["source"] == "clear" })
	s.WaitQuiet(3*time.Second, 30*time.Second)
	s.Type("I'm back. Undo whatever was done while I was away.")
	waitSequence(t, s, 2*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "turn_started" && e["by"] == "human" },
		func(e map[string]any) bool { return e["kind"] == "decisions_told" && fmt.Sprint(e["ids"]) == "[d1]" },
	)
	if !s.Until("greeting.txt removed", 3*time.Minute, func() bool { _, err := os.Stat(greeting); return os.IsNotExist(err) }) {
		t.Fatalf("the late undo did not undo it; events: %v", s.Events())
	}
}
