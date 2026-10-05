//go:build !windows

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// waitSequence waits until the events contain each step in order (other events may come between).
func waitSequence(t *testing.T, s *Session, timeout time.Duration, steps ...func(map[string]any) bool) {
	t.Helper()
	next := 0
	s.Until("event sequence", timeout, func() bool {
		next = 0
		for _, e := range s.Events() {
			if next < len(steps) && steps[next](e) {
				next++
			}
		}
		return next == len(steps)
	})
	if next != len(steps) {
		t.Fatalf("matched %d of %d steps; events:\n%s", next, len(steps), eventLog(s))
	}
}

// eventLog is the session's events, one a line, for a failure message.
func eventLog(s *Session) string {
	var lines []string
	for _, e := range s.Events() {
		delete(e, "ts")
		delete(e, "instance")
		lines = append(lines, fmt.Sprint(e))
	}
	return strings.Join(lines, "\n")
}

func kind(k string) func(map[string]any) bool {
	return func(e map[string]any) bool { return e["kind"] == k }
}

// The human interjects with a question; the model answers and stops without a status. baton refuses the
// stop, and the model goes back to the phase and reports it.
func TestSilentStopIsRefusedThenReported(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := Start(t, dir, "--model", "haiku", "Quick question before anything else: what is 2+2? Answer in one word.")
	s.Trust()
	waitSequence(t, s, 3*time.Minute,
		kind("stop_refused"),
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		kind("compact_queued"),
	)
}

// Blocked: the human is asked in session (a question that reaches every device) and pushed out of band;
// answering "Continue" resumes the plan.
func TestBlockedAsksTheHumanAndResumes(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	s := Start(t, dir, "--model", "haiku",
		`This phase needs a decision only the human can make. Run exactly: baton blocked "need the human to pick a greeting" --tried "the plan does not say which greeting" — then end your turn and follow baton's instructions.`)
	s.Trust()
	waitSequence(t, s, 3*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "escalated" && e["type"] == "blocked" },
		func(e map[string]any) bool { return e["kind"] == "dialog_open" && e["tool"] == "AskUserQuestion" },
	)
	time.Sleep(2 * time.Second)
	s.pty.Write([]byte("\r")) // the human picks the first option: "Continue"
	waitSequence(t, s, 2*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "notified" && e["notice"] == "blocked" },
		kind("resumed"),
	)
}
