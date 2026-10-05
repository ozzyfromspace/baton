package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// attached is a hosted session with the test plan attached and owned, P0 under way.
func attached(t *testing.T) *session {
	s, planFile := newSession(t)
	s.attach(planFile)
	s.env["BATON_INSTANCE"] = "inst"
	s.set(func(x *state.State) { state.Claim(x, "inst", 1, s.now) })
	return s
}

func (s *session) set(fn func(*state.State)) {
	st, _ := state.Open(s.env["BATON_DIR"], "", nil)
	st.Update(func(x *state.State) error { fn(x); return nil })
}

// logged reports an event of kind whose fields include want.
func (s *session) logged(kind string, want map[string]any) bool {
	for _, line := range strings.Split(s.events(), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil || ev["kind"] != kind {
			continue
		}
		match := true
		for k, v := range want {
			if ev[k] != v {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (s *session) state() state.State {
	st, _ := state.Open(s.env["BATON_DIR"], "", nil)
	x, _ := st.Load()
	return x
}

func TestNotesCountTowardAReview(t *testing.T) {
	s := attached(t)
	s.env["BATON_MAX_AUTO_DECISIONS"] = "2"
	out := s.must("", "note", "committed P0 unsigned: gpg times out", "--undo", "git commit --amend --no-edit -S")
	if !strings.Contains(out, "noted (d1, 1 of 2 decisions without the human in P0); the human will be told. Carry on with P0.") {
		t.Fatalf("note: %s", out)
	}
	st := s.state()
	if d := st.Decisions; len(d) != 1 || d[0].What != "committed P0 unsigned: gpg times out" || d[0].Undo != "git commit --amend --no-edit -S" {
		t.Fatalf("decisions %+v", d)
	}
	if n := st.Run.Notices; len(n) != 1 || n[0].Kind != "note" {
		t.Fatalf("notices %+v", n)
	}

	out = s.must("", "note", "skipped the flaky e2e test")
	if !strings.Contains(out, "noted (d2, 2 of 2 decisions without the human in P0). That is as many as baton lets a phase make") || !strings.Contains(out, "end your turn now") {
		t.Fatalf("note at the cap: %s", out)
	}
	if st := s.state(); st.ReviewDue != "P0" {
		t.Fatalf("review due %q", st.ReviewDue)
	}
	for _, args := range [][]string{{"note", "one more"}, {"propose", "x", "--because", "y", "--undo", "z"}} {
		if why := s.fails(args...); !strings.Contains(why, "as many decisions without the human as baton allows") {
			t.Errorf("%v while a review is due: %s", args, why)
		}
	}
	if !s.logged("note", map[string]any{"id": "d1", "phase": "P0", "undo": "git commit --amend --no-edit -S"}) ||
		!s.logged("review_due", map[string]any{"phase": "P0", "decisions": 2.0}) || !s.logged("refused", map[string]any{"command": "note", "why": "review due"}) {
		t.Errorf("events: %s", s.events())
	}

	// No cap: the count alone.
	s = attached(t)
	s.env["BATON_MAX_AUTO_DECISIONS"] = "0"
	if out := s.must("", "note", "a"); !strings.Contains(out, "noted (d1, 1 decision without the human in P0)") {
		t.Errorf("no cap: %s", out)
	}
}

func TestProposalRefusals(t *testing.T) {
	ok := []string{"propose", "commit P0 unsigned", "--because", "gpg times out", "--undo", "git commit --amend --no-edit -S"}
	for _, c := range []struct {
		name string
		set  func(*session)
		args []string
		want string
		why  string
	}{
		{"no action", nil, []string{"propose", "--because", "x", "--undo", "y"}, "usage: baton propose", ""},
		{"no because", nil, []string{"propose", "x", "--undo", "y"}, "Waiting, doing nothing or stopping is not a proposal", ""},
		{"no undo", nil, []string{"propose", "x", "--because", "y"}, "can be undone", ""},
		{"not hosted", func(s *session) { delete(s.env, "BATON_HOST") }, ok, "not hosted by baton", "not hosted"},
		{"paused", func(s *session) { s.set(func(x *state.State) { state.Pause(x) }) }, ok, "not running the plan (mode: paused)", "not running"},
		{"compaction owed", func(s *session) { s.set(func(x *state.State) { x.CheckpointOwed = true }) }, ok, "must compact the context first", "compaction owed"},
		{"review due", func(s *session) { s.set(func(x *state.State) { x.ReviewDue = "P0" }) }, ok, "before they review them", "review due"},
		{"one not yet asked", func(s *session) { s.must("", ok...) }, ok, "is still waiting to be put to the human. Put it to them first: call the AskUserQuestion tool", "proposal pending"},
		{"one asked", func(s *session) {
			s.must("", ok...)
			s.set(func(x *state.State) { x.PendingProposal().Asked = true })
		}, ok, "(commit P0 unsigned) is still waiting for the human's answer. One proposal at a time", "proposal pending"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := attached(t)
			if c.set != nil {
				c.set(s)
			}
			before := len(s.state().Decisions)
			if why := s.fails(c.args...); !strings.Contains(why, c.want) {
				t.Errorf("refusal lacks %q:\n%s", c.want, why)
			}
			if c.why != "" && !s.logged("refused", map[string]any{"command": "propose", "why": c.why}) {
				t.Errorf("no refused event: %s", s.events())
			}
			if n := len(s.state().Decisions); n != before {
				t.Errorf("a refused proposal was recorded")
			}
		})
	}
}

var (
	callQuestion = regexp.MustCompile(`exactly one question, "([^"]*)" \(header "baton"\)`)
	callLabel    = regexp.MustCompile(`"([^"]*)" \(description: "[^"]*"\)`)
)

// askFrom builds the AskUserQuestion input a model would send after reading baton's instructions.
func askFrom(t *testing.T, instructions string) map[string]any {
	t.Helper()
	m := callQuestion.FindStringSubmatch(instructions)
	if m == nil {
		t.Fatalf("no question in %s", instructions)
	}
	var opts []any
	for _, l := range callLabel.FindAllStringSubmatch(instructions, -1) {
		opts = append(opts, map[string]any{"label": l[1], "description": "…"})
	}
	return map[string]any{"session_id": "s", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
		"questions": []any{map[string]any{"question": m[1], "header": "baton", "options": opts, "multiSelect": false}},
	}}
}

// The question baton prints is one the hooks recognize as its own: PreToolUse lets it through mid-run,
// and its dialog is marked as the proposal's.
func TestAProposalPrintsTheQuestionBatonWillRecognize(t *testing.T) {
	s := attached(t)
	s.env["BATON_ESCALATION_TIMEOUT"] = "90s"
	out := s.must("", "propose", `commit P0 "unsigned"`, "--because", "gpg signing\ntimes out.", "--undo", `git commit --amend --no-edit -S`, "--tried", "gpgconf --kill gpg-agent")
	by := time.Date(2026, 10, 4, 12, 2, 0, 0, time.UTC).Local().Format("15:04") // 12:00 + 90s, rounded up
	question := "baton: gpg signing times out. Unless you answer by " + by + ", I will commit P0 'unsigned'."
	for _, want := range []string{"proposal d1 recorded. Now put it to the human: call the AskUserQuestion tool with exactly one question, \"" + question + "\"",
		`"Wait for me" (description:`, `"Pause baton" (description:`, `"Go ahead" (description:`, "If nobody answers by " + by + ", baton picks Go ahead"} {
		if !strings.Contains(out, want) {
			t.Errorf("propose lacks %q:\n%s", want, out)
		}
	}
	st := s.state()
	d := st.PendingProposal()
	if d == nil || d.Question != question || d.Tried != "gpgconf --kill gpg-agent" || d.Untimed != "" || !d.Deadline.Equal(time.Date(2026, 10, 4, 12, 2, 0, 0, time.UTC)) {
		t.Fatalf("pending %+v", d)
	}
	if !s.logged("proposed", map[string]any{"id": "d1", "deadline": "2026-10-04T12:02:00Z", "tried": "gpgconf --kill gpg-agent"}) {
		t.Errorf("events: %s", s.events())
	}

	in, _ := json.Marshal(askFrom(t, out))
	if code, hookOut, _ := s.run(string(in), "hook", "PreToolUse"); code != 0 || strings.Contains(hookOut, "deny") {
		t.Fatalf("PreToolUse refused baton's own question: %s", hookOut)
	}
	s.must(string(in), "hook", "PermissionRequest")
	if q, open := s.state().Run.Dialogs.Front(); !open || q.Kind != "proposal" {
		t.Fatalf("dialog %+v", q)
	}
}

// A proposal that looks outward-facing or irreversible gets no deadline: only the human can let it go.
func TestAnOutwardProposalHasNoTimer(t *testing.T) {
	s := attached(t)
	out := s.must("", "propose", "push the hotfix branch", "--because", "the fix is ready", "--undo", "git push --delete origin hotfix")
	question := "baton: the fix is ready. I propose to push the hotfix branch, and will not do it without you ('push' looks outward-facing or irreversible)."
	if !strings.Contains(out, "with no deadline: 'push' looks outward-facing or irreversible, so only the human can let it go ahead") || !strings.Contains(out, `"`+question+`"`) || strings.Contains(out, "If nobody answers") {
		t.Fatalf("propose: %s", out)
	}
	if st := s.state(); st.PendingProposal() == nil || st.PendingProposal().Untimed == "" || !st.PendingProposal().Deadline.IsZero() {
		t.Fatalf("pending %+v", st.Decisions)
	}
	if !s.logged("proposed", map[string]any{"untimed": "'push' looks outward-facing or irreversible"}) {
		t.Errorf("events: %s", s.events())
	}
}

func TestBlockedNeedsWhatWasTried(t *testing.T) {
	s := attached(t)
	why := s.fails("blocked", "need", "the", "prod", "key")
	for _, want := range []string{"blocked stops the whole run until the human answers, so it needs --tried", "baton note", "baton propose", "A proposal is an action that makes progress"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal lacks %q:\n%s", want, why)
		}
	}
	if st := s.state(); st.Blocked != nil {
		t.Fatal("recorded without --tried")
	}
	if !s.logged("refused", map[string]any{"command": "blocked", "why": "no --tried"}) {
		t.Errorf("events: %s", s.events())
	}
	s.fails("blocked", "--tried", "x") // no reason
	s.must("", "blocked", "need", "the", "prod", "key", "--tried", "the vault and .env")
	if b := s.state().Blocked; b == nil || b.Reason != "need the prod key" || b.Tried != "the vault and .env" {
		t.Fatalf("blocked %+v", b)
	}
	if !s.logged("blocked", map[string]any{"reason": "need the prod key", "tried": "the vault and .env"}) {
		t.Errorf("events: %s", s.events())
	}
}
