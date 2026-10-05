package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// propose records a pending proposal as `baton propose` would, and returns the exact question call baton
// told the model to make. untimed gives it no deadline.
func (f *fixture) propose(untimed bool) map[string]any {
	f.t.Helper()
	d := state.Decision{What: "commit P0 unsigned", Because: "gpg signing times out", Undo: "git commit --amend --no-edit -S"}
	var q decide.Question
	if untimed {
		d.What, d.Untimed = "push the branch", decide.UntimedReason("push")
		q = decide.UntimedQuestion(d.Because, d.What, d.Untimed)
	} else {
		d.Deadline = decide.Deadline(f.now, 5*time.Minute)
		q = decide.ProposalQuestion(d.Because, d.What, d.Deadline)
	}
	d.Question = q.Text
	if _, err := f.store.Update(func(st *state.State) error { _, err := state.AddProposal(st, d, f.now); return err }); err != nil {
		f.t.Fatal(err)
	}
	return ask(q.Text, "Wait for me", "Pause baton", "Go ahead")
}

// events lists the events logged so far, without their timestamps and instance.
func (f *fixture) events() []map[string]any {
	b, _ := os.ReadFile(filepath.Join(f.dir, "events.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// logged reports an event of kind whose fields include every one in want.
func (f *fixture) logged(kind string, want map[string]any) bool {
	for _, e := range f.events() {
		if e["kind"] != kind {
			continue
		}
		match := true
		for k, v := range want {
			if e[k] != v {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

func noticeKinds(st state.State) []string {
	var out []string
	for _, n := range st.Run.Notices {
		out = append(out, n.Kind)
	}
	return out
}

// The model puts the proposal to the human; baton marks it asked and pushes. Whatever the answer, baton
// acts on it itself: the model is told what to do next, the record says who decided, and only the
// human's own answer counts as the human taking part.
func TestAProposalIsPutToTheHumanAndActedOn(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		auto         bool
		by           string
		mode         string
		tell         []string
		notices      []string // queued after the proposal's own
		held         bool
	}{
		{name: "the human says go ahead", answer: "Go ahead", by: state.ByHuman, mode: state.ModeRunning,
			tell: []string{"The human chose Go ahead", "Do what you proposed now: commit P0 unsigned"}},
		{name: "nobody answers by the deadline", answer: "Go ahead", auto: true, by: state.ByTimeout, mode: state.ModeRunning,
			tell:    []string{"Nobody answered", "Do what you proposed now: commit P0 unsigned", "undone with: git commit --amend --no-edit -S", "carry on with P0"},
			notices: []string{"proceeded"}},
		{name: "wait for me", answer: "Wait for me", by: state.ByHeld, mode: state.ModeRunning,
			tell: []string{"do not do what you proposed", "End your turn now"}, notices: []string{"decision"}, held: true},
		{name: "pause baton", answer: "Pause baton", by: state.ByPaused, mode: state.ModePaused,
			tell: []string{"baton is paused", "is not to be done"}},
		{name: "an answer in the human's own words", answer: "use the staging key instead", by: state.ByHuman, mode: state.ModeRunning,
			tell: []string{`"use the staging key instead"`, "not a Go ahead"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			call := f.propose(false)
			if out := f.fire("PreToolUse", call); denyReason(out) != "" {
				t.Fatalf("the issued question was refused: %s", denyReason(out))
			}
			f.fire("PermissionRequest", call)
			st := f.state()
			if p := st.PendingProposal(); p == nil || !p.Asked || st.Run.Dialogs[0].Kind != "proposal" {
				t.Fatalf("not asked: %+v %+v", p, st.Run.Dialogs)
			}
			if !f.logged("proposal_asked", map[string]any{"id": "d1"}) || strings.Join(noticeKinds(st), ",") != "proposal" {
				t.Fatalf("asked: notices %v, events %v", noticeKinds(st), f.events())
			}
			if c.auto {
				f.store.Update(func(st *state.State) error { st.Run.Dialogs[0].AutoAnswered = f.now; return nil })
			}
			f.now = f.now.Add(time.Minute)
			out := f.fire("PostToolUse", answer(call, c.answer))

			st = f.state()
			d := st.Decisions[0]
			if d.Resolved == nil || d.Resolved.By != c.by || d.Resolved.Answer != c.answer || st.PendingProposal() != nil {
				t.Fatalf("resolved %+v", d.Resolved)
			}
			if !f.logged("proposal_resolved", map[string]any{"id": "d1", "by": c.by, "answer": c.answer}) {
				t.Errorf("no proposal_resolved: %v", f.events())
			}
			for _, want := range c.tell {
				if !strings.Contains(additionalContext(out), want) {
					t.Errorf("the model was not told %q:\n%s", want, additionalContext(out))
				}
			}
			if say, _ := out["systemMessage"].(string); say == "" {
				t.Error("nothing said in the session")
			}
			if got, want := strings.Join(noticeKinds(st)[1:], ","), strings.Join(c.notices, ","); got != want {
				t.Errorf("notices %q, want %q", got, want)
			}
			if st.Mode != c.mode {
				t.Errorf("mode %s, want %s", st.Mode, c.mode)
			}
			if human := st.Run.HumanAt.Equal(f.now); human == c.auto {
				t.Errorf("auto=%v but human at %v", c.auto, st.Run.HumanAt)
			}
			if _, _, held := st.Held(); held != c.held {
				t.Errorf("held %v, want %v", held, c.held)
			}
			if c.held {
				if e := st.Run.Escalation; e == nil || e.Kind != "decision" || !e.Asked || e.Question != "" {
					t.Fatalf("escalation %+v", e)
				}
				// The model ends its turn: no second question, the run waits.
				stop := f.fire("Stop", map[string]any{})
				if stop["decision"] == "block" || !strings.Contains(stop["systemMessage"].(string), "waiting on you") {
					t.Fatalf("stop: %v", stop)
				}
			}
		})
	}
}

// A proposal whose question goes away unanswered (Esc, an interrupted turn, a restart) is declined, and
// held for the human like Wait for me. Nothing anyone said was Go ahead.
func TestAProposalThatVanishesIsHeld(t *testing.T) {
	for _, c := range []struct {
		event string
		input map[string]any
	}{
		{"Stop", map[string]any{}},
		{"StopFailure", map[string]any{"error": "server_error"}},
		{"Notification", map[string]any{"notification_type": "idle_prompt"}},
		{"SessionStart", map[string]any{"source": "resume"}},
		{"UserPromptSubmit", map[string]any{"prompt": NudgePrefix + " No activity for 10m."}},
		{"PostToolUseFailure", nil}, // the question's own call failed
	} {
		t.Run(c.event, func(t *testing.T) {
			f := newFixture(t, true)
			call := f.propose(false)
			f.fire("PermissionRequest", call)
			input := c.input
			if input == nil {
				input = call
			}
			f.fire(c.event, input)
			st := f.state()
			if r := st.Decisions[0].Resolved; r == nil || r.By != state.ByDeclined {
				t.Fatalf("resolved %+v", r)
			}
			if e := st.Run.Escalation; e == nil || e.Kind != "decision" || !e.Asked {
				t.Fatalf("escalation %+v", e)
			}
			if _, _, held := st.Held(); !held {
				t.Error("not held for the human")
			}
			if !f.logged("proposal_resolved", map[string]any{"by": state.ByDeclined}) || !f.logged("escalated", map[string]any{"type": "decision"}) {
				t.Errorf("events: %v", f.events())
			}
		})
	}

	// The human typing instead of answering is there to answer: nothing is held, and the model is told
	// their message is the answer.
	f := newFixture(t, true)
	f.fire("PermissionRequest", f.propose(false))
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "no, keep it signed"})
	st := f.state()
	if r := st.Decisions[0].Resolved; r == nil || r.By != state.ByDeclined || st.Run.Escalation != nil {
		t.Fatalf("resolved %+v, escalation %+v", r, st.Run.Escalation)
	}
	if !strings.Contains(additionalContext(out), "their message is the answer") {
		t.Fatalf("context: %v", out)
	}

	// A question still on screen (its PostToolUse not yet in) is not swept by a subagent finishing.
	f = newFixture(t, true)
	f.fire("PermissionRequest", f.propose(false))
	f.fire("SubagentStop", map[string]any{"agent_id": "sub-1"})
	if st := f.state(); st.PendingProposal() == nil {
		t.Fatal("a subagent's end settled the main agent's proposal")
	}
}

// A proposal held for the human ends when they next write in the session: their message is its answer,
// and the model may act on it.
func TestAHumanPromptReleasesAHold(t *testing.T) {
	f := newFixture(t, true)
	call := f.propose(false)
	f.fire("PermissionRequest", call)
	f.fire("PostToolUse", answer(call, "Wait for me"))
	f.fire("Stop", map[string]any{})

	// baton's own nudges do not release it.
	f.now = f.now.Add(time.Hour)
	f.fire("UserPromptSubmit", map[string]any{"prompt": NudgePrefix + " Still there?"})
	if _, _, held := f.state().Held(); !held {
		t.Fatal("a nudge released the hold")
	}
	out := f.fire("UserPromptSubmit", map[string]any{"prompt": "fine, but sign it tomorrow"})
	st := f.state()
	if _, _, held := st.Held(); held || st.Run.Escalation != nil {
		t.Fatalf("still held: %+v", st.Run.Escalation)
	}
	if !strings.Contains(additionalContext(out), "Your proposal d1 (commit P0 unsigned)") {
		t.Fatalf("context: %v", out)
	}
	if !f.logged("hold_released", map[string]any{"id": "d1", "by": "human"}) {
		t.Fatalf("events: %v", f.events())
	}
}

// Until a proposal is put to the human, nothing else runs. baton's CLI and baton's own question get
// through, and subagents are never held.
func TestToolsAreHeldUntilAProposalIsAsked(t *testing.T) {
	f := newFixture(t, true)
	call := f.propose(false)
	if r := denyReason(f.fire("PreToolUse", bash("ls"))); !strings.Contains(r, "put your proposal d1 to the human first") || !strings.Contains(r, "AskUserQuestion") {
		t.Fatalf("not held: %q", r)
	}
	for name, in := range map[string]map[string]any{
		"baton's CLI": bash("baton status"), "the issued question": call, "a subagent": byAgent(bash("ls"), "sub-1"),
	} {
		if r := denyReason(f.fire("PreToolUse", in)); r != "" {
			t.Errorf("%s held: %s", name, r)
		}
	}
	f.fire("PermissionRequest", call)
	if r := denyReason(f.fire("PreToolUse", bash("ls"))); r != "" {
		t.Errorf("held once asked: %s", r)
	}

}

// No other question of baton's while a proposal waits on the human: one at a time.
func TestNoContextQuestionWhileAProposalWaits(t *testing.T) {
	f := newFixture(t, true)
	f.vars = defaultCap
	f.propose(false)
	f.setContext(750_000)
	if out := f.fire("PostToolUse", bash("ls")); additionalContext(out) != "" {
		t.Fatalf("asked while a proposal waits: %v", out)
	}
	if f.state().Run.WarnQuestion != "" {
		t.Fatal("a context question was issued")
	}
}

// A note is shown in the session once, after the command that recorded it.
func TestANoteIsAnnouncedOnce(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(st *state.State) error {
		_, err := state.AddNote(st, "committed P0 unsigned", "re-sign it", f.now)
		return err
	})
	out := f.fire("PostToolUse", bash(`baton note "committed P0 unsigned" --undo "re-sign it"`))
	if say, _ := out["systemMessage"].(string); say != "baton: noted — committed P0 unsigned" {
		t.Fatalf("said %q", say)
	}
	if !f.state().Decisions[0].Announced {
		t.Fatal("not marked announced")
	}
	if out := f.fire("PostToolUse", bash("ls")); out != nil {
		t.Fatalf("announced twice: %v", out)
	}
}
