//go:build !windows

package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A phase's id is the human's handle on baton's agent on it: `@P1 <words>` at the lead's prompt writes to
// P1's agent (spike 40). The lead's UserPromptSubmit hook keeps the prompt from the lead's model and
// queues the words, the host sends them through baton's mod, and the mod empties the input box of the
// prompt. A phase with no agent at work, a done one among them, gets the words through the lead's model.

// handlesPlan: the lead takes P0, and P1's agent runs hold.sh in the foreground until the test releases
// it, so it is at work while the human writes to it.
const handlesPlan = `# Handles plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Wait, then write one
After: none

1. Run exactly this command with the Bash tool, with its timeout set to 600000 milliseconds: sh hold.sh
2. Write the single word one into a new file named one.txt.
3. Commit your work: git add -A, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'wrote one'
`

// toAgent is what the human writes to P1's agent; toLead is what they write about P0, which is done.
const (
	toAgent = "also write the single word kiwi into a new file named fruit.txt"
	toLead  = "reply with only the word noted, and do nothing else"
)

// The human writes `@P1 <words>` at the lead's prompt while P1's agent works: the words are queued, sent
// to the agent through the mod, and acked; the input box is emptied of the prompt; the agent does what
// they ask, and its work lands with fruit.txt in it; and nothing of the words reaches the lead's model.
// Then `@P0 <words>`, for a phase the lead has done: nothing is woken, and the prompt is the lead's.
func TestTheHumanWritesToAPhaseAgentFromTheLeadsPrompt(t *testing.T) {
	dir := projectWith(t, map[string]string{"hold.sh": holdScript})
	Attach(t, dir, handlesPlan)
	s := StartAgents(t, dir, nil, "--allowedTools", "Bash(sh hold.sh)", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Write")
	s.AutoApprove()

	if spawned := s.WaitEvent("worker_spawned", 2*time.Minute); spawned == nil || spawned["phase"] != "P1" {
		t.Fatalf("no phase agent on P1: %v", spawned)
	}
	if !s.Until("hold.sh to start", 5*time.Minute, func() bool { return readFile(filepath.Join(dir, ".git"), "hold.pid") != "" }) {
		t.Fatal("P1's agent never ran hold.sh")
	}

	s.Type("@P1 " + toAgent)
	waitSequence(t, s, 2*time.Minute,
		is("human_to_agent", "phase", "P1", "routed", "agent", "step", "queued"),
		is("human_to_agent", "phase", "P1", "step", "sent"),
		is("human_to_agent", "phase", "P1", "step", "acked", "ok", "true"),
	)
	if !s.Until("the input box to be emptied", time.Minute, func() bool { return first(s.Events(), is("mod_command", "command", "empty")) >= 0 }) {
		t.Error("the mod was never asked to empty the input box")
	}

	if !s.Until("the lead to finish P0", 3*time.Minute, func() bool { return first(s.Events(), is("phase_done", "phase", "P0")) >= 0 }) {
		t.Fatal("the lead never finished P0")
	}
	s.WaitQuiet(3*time.Second, 30*time.Second) // P1's agent may keep the screen moving
	s.Type("@P0 " + toLead)
	waitSequence(t, s, time.Minute, is("human_to_agent", "phase", "P0", "routed", "lead", "stands", "done"))
	lead := leadTranscriptOf(s)
	if lead == "" {
		t.Fatal("no sub-run of P1 names its agent's transcript, beside the lead's")
	}
	// A prompt typed while the lead's turn runs reaches its model as a queued command's attachment.
	if !s.Until("the lead's model to get the prompt about P0", 2*time.Minute, func() bool {
		return len(entriesWith(lead, toLead, "user", "attachment")) > 0
	}) {
		t.Error("the prompt about P0, which is done, never reached the lead's model")
	}

	release(t, dir)
	s.Until("the plan to complete", 6*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 })
	ev := s.Events()
	if first(ev, is("landed", "phase", "P1")) < 0 {
		t.Fatal("P1's work did not land")
	}
	if got := strings.TrimSpace(readFile(dir, "fruit.txt")); got != "kiwi" {
		t.Errorf("fruit.txt in the checkout, which the human asked P1's agent for: %q", got)
	}
	if got := strings.TrimSpace(readFile(dir, "one.txt")); got != "one" {
		t.Errorf("one.txt in the checkout: %q", got)
	}
	if n := count(ev, is("human_to_agent", "phase", "P1", "step", "queued")); n != 1 {
		t.Errorf("the words for P1 were queued %d times", n)
	}
	// The lead's model never had the words for P1: no turn of its holds them, nor any request it sent.
	// Claude Code shows the human the blocked prompt in a system entry of its own, which no request carries.
	if leaked := entriesWith(lead, toAgent, "user", "assistant", "attachment", "api-request-blob"); len(leaked) > 0 {
		t.Errorf("the lead's transcript carries the words for P1's agent: %v", leaked)
	}
	none(t, s, "controller_panic", "retired_agent_woke")
}

// leadTranscriptOf is the lead's transcript of the run in s's project, or "": beside the transcript of a
// phase agent, which its sub-run records (subagents/agent-<id>.jsonl under the lead's session's folder).
func leadTranscriptOf(s *Session) string {
	paths, _ := filepath.Glob(filepath.Join(s.Dir, ".baton", "runs", "*", "workers", "*", "state.json"))
	for _, p := range paths {
		var st struct {
			Run struct {
				Transcript string `json:"transcript"`
			} `json:"run"`
		}
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &st) == nil && st.Run.Transcript != "" {
			return filepath.Dir(filepath.Dir(st.Run.Transcript)) + ".jsonl"
		}
	}
	return ""
}

// entriesWith is the entries of the transcript at path, of the types given, that hold text, each cut to
// a line for a failure's message.
func entriesWith(path, text string, types ...string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, text) {
			continue
		}
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		for _, ty := range types {
			if e.Type == ty {
				out = append(out, tail(line, 300))
			}
		}
	}
	return out
}
