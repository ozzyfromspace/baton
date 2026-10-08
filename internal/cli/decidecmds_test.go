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
	st := s.store()
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

// human is the human taking part: a prompt they typed, or their answer to one of baton's questions.
func (s *session) human() {
	s.now = s.now.Add(time.Second)
	s.set(func(x *state.State) { x.Run.HumanAt = s.now })
}

func (s *session) state() state.State {
	st := s.store()
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
	// The run cannot let itself go on: only the human ends a review.
	for _, args := range [][]string{{"run"}, {"done", "P0"}} {
		if why := s.fails(args...); !strings.Contains(why, "a review of the decisions P0 made without them") {
			t.Errorf("%v during a review: %s", args, why)
		}
	}
	s.human()
	s.must("", "run")
	if st := s.state(); st.ReviewDue != "" || st.Unattended("P0") != 0 {
		t.Fatalf("after the human resumed: %q, %d unreviewed", st.ReviewDue, st.Unattended("P0"))
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
		{"one not yet asked", func(s *session) { s.must("", ok...) }, ok, "not recorded — put your proposal d1 to the human first: call the AskUserQuestion tool", "proposal pending"},
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
	return map[string]any{"session_id": "sess-1", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
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

// While the run waits on the human, the model cannot get out of it with a command of its own.
func TestARunCannotClearItsOwnBlock(t *testing.T) {
	commands := [][]string{{"run"}, {"done", "P0"}, {"checkpoint"}, {"waiting", "the build", "--until", "5m"},
		{"blocked", "still stuck", "--tried", "more"}, {"propose", "x", "--because", "y", "--undo", "z"}}
	halts := map[string]func(s *session){
		"blocked": func(s *session) { s.must("", "blocked", "need the prod key", "--tried", "the vault") },
		"a held proposal": func(s *session) {
			s.set(func(x *state.State) {
				x.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "the human is deciding", Since: s.now}
			})
		},
		"a review": func(s *session) {
			s.set(func(x *state.State) {
				x.Run.Escalation = &state.Escalation{Kind: "review", Reason: "review due", Since: s.now}
			})
		},
	}
	for name, halt := range halts {
		for _, args := range commands {
			t.Run(name+"/"+args[0], func(t *testing.T) {
				s := attached(t)
				halt(s)
				before := s.state()
				why := s.fails(args...)
				if !strings.Contains(why, "the run is waiting on the human (") || !strings.Contains(why, "only they can clear that. End your turn now") {
					t.Fatalf("refusal: %s", why)
				}
				if !s.logged("refused", map[string]any{"command": args[0], "why": "held for the human"}) {
					t.Errorf("events: %s", s.events())
				}
				if after := s.state(); after.Mode != before.Mode || (after.Blocked == nil) != (before.Blocked == nil) ||
					(after.Run.Escalation == nil) != (before.Run.Escalation == nil) || after.CheckpointOwed || after.Waiting != nil || len(after.Decisions) > 0 {
					t.Fatalf("a refused %s changed the state: %+v", args[0], after)
				}

				// Once the human has taken part, the model may act on what they said.
				s.human()
				if args[0] == "run" && name != "blocked" {
					return // only a block or a pause is resumed from the CLI
				}
				if code, _, errs := s.run("", args...); code != 0 {
					t.Fatalf("after the human: %s", errs)
				}
			})
		}
	}

	// From the human's own shell, nothing is held.
	s := attached(t)
	halts["blocked"](s)
	delete(s.env, "BATON_HOST")
	s.must("", "run")
}

// A proposal goes to the human before anything else moves.
func TestAProposalIsAskedBeforeAnythingElse(t *testing.T) {
	s := attached(t)
	s.must("", "propose", "skip the flaky test", "--because", "it times out on CI", "--undo", "re-enable it")
	for _, args := range [][]string{{"done", "P0"}, {"checkpoint"}, {"waiting", "CI", "--until", "5m"}, {"blocked", "x", "--tried", "y"}} {
		if why := s.fails(args...); !strings.Contains(why, "put your proposal d1 to the human first: call the AskUserQuestion tool with exactly one question, \"baton: it times out on CI.") {
			t.Errorf("%v: %s", args, why)
		}
		if !s.logged("refused", map[string]any{"command": args[0], "why": "proposal not asked"}) {
			t.Errorf("%v: events %s", args, s.events())
		}
	}
	s.must("", "note", "notes still go through") // a note stops nothing
	s.set(func(x *state.State) { x.PendingProposal().Asked = true })
	s.must("", "checkpoint")
}

// baton status is where the human finds what the run decided without them, each with its undo, and what
// waits on them.
func TestStatusListsTheDecisionsMadeWithoutYou(t *testing.T) {
	s := attached(t)
	if out := s.must("", "status"); strings.Contains(out, "decisions made without you") || strings.Contains(out, "proposal") {
		t.Fatalf("status with no decisions:\n%s", out)
	}
	noted := s.now
	s.must("", "note", "committed P0 unsigned: gpg times out", "--undo", "git commit --amend --no-edit -S")
	s.now = s.now.Add(time.Minute)
	s.must("", "propose", "skip the flaky test", "--because", "it times out on CI", "--undo", "re-enable it")
	st := s.state()
	deadline := st.PendingProposal().Deadline
	record := "decisions made without you (this run, oldest first):\n  d1   P0       " + noted.Local().Format("Jan 2 15:04") +
		"  noted: committed P0 unsigned: gpg times out\n       undo: git commit --amend --no-edit -S\n"

	out := s.must("", "status")
	for _, want := range []string{"proposal d2: skip the flaky test\n  because: it times out on CI\n  goes ahead at " + deadline.Local().Format("15:04") +
		" unless you answer (Claude has yet to put the question to you)\n  undo: re-enable it\n", record} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	s.set(func(x *state.State) { x.PendingProposal().Asked = true })
	if out := s.must("", "status"); !strings.Contains(out, "unless you answer baton's question in the session") {
		t.Errorf("asked:\n%s", out)
	}

	// Nobody answered: it went ahead, and joins the record.
	wentAhead := deadline.Add(time.Minute)
	s.set(func(x *state.State) { state.Resolve(x, "d2", state.ByTimeout, "Go ahead", wentAhead) })
	out = s.must("", "status")
	if want := record + "  d2   P0       " + wentAhead.Local().Format("Jan 2 15:04") + "  went ahead, as nobody answered by " +
		deadline.Local().Format("15:04") + ": skip the flaky test\n       undo: re-enable it\n"; !strings.Contains(out, want) || strings.Contains(out, "proposal d2:") {
		t.Errorf("status lacks %q:\n%s", want, out)
	}

	// One the human answered is not theirs to find here; one held for them, and a review, wait on them.
	s.must("", "propose", "use the staging database", "--because", "the dev one is down", "--undo", "switch back")
	s.set(func(x *state.State) {
		state.Resolve(x, "d3", state.ByHeld, "Wait for me", s.now)
		x.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "proposal d3 waits for you: use the staging database", Since: s.now, Asked: true}
		state.DueReview(x, "P0", s.now)
	})
	out = s.must("", "status")
	for _, want := range []string{"held for you: proposal d3 waits for you: use the staging database — write in the session to answer it",
		"review due: P0 has made 2 decisions without you, as many as it may before you go over them (below). Answer baton's question in the session, or /baton run"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "  d3 ") {
		t.Errorf("a proposal the human answered is listed as made without them:\n%s", out)
	}

	var status struct {
		State struct{ Decisions []state.Decision } `json:"state"`
	}
	json.Unmarshal([]byte(s.must("", "status", "--json")), &status)
	if d := status.State.Decisions; len(d) != 3 || d[1].Resolved == nil || d[1].Resolved.By != state.ByTimeout || d[0].Undo != "git commit --amend --no-edit -S" {
		t.Errorf("--json decisions: %+v", d)
	}
}

// baton done lists the decisions that phase made without the human, and the last one counts the run's.
func TestDoneListsTheDecisionsMadeWithoutTheHuman(t *testing.T) {
	s := attached(t) // not a git repository: nothing here depends on git
	s.must("", "note", "pinned the linter to v1", "--undo", "unpin it")
	out := s.must("", "done", "P0")
	list := "baton: P0 made 1 decision without the human (baton status lists them for the human, each with its undo):\n- d1 (P0, " +
		s.now.Local().Format("Jan 2 15:04") + ") noted: pinned the linter to v1. Undo: unpin it\n"
	if !strings.Contains(out, list+"baton: P0 done. Next phase: P1") {
		t.Errorf("done P0:\n%s", out)
	}
	s.startNext()
	s.must("", "note", "a")
	s.must("", "note", "b")
	if out := s.must("", "done", "P1"); !strings.Contains(out, "P1 made 2 decisions without the human") || !strings.Contains(out, "- d2 (P1,") ||
		!strings.Contains(out, "- d3 (P1,") || strings.Contains(out, "d1") {
		t.Errorf("done P1:\n%s", out)
	}
	s.startNext()
	out = s.must("", "done", "R1")
	if strings.Contains(out, "R1 made") || !strings.Contains(out, "the plan is complete. Summarize the results for the human, including the 3 decisions this run made without them "+
		"(baton status lists them, each with its undo), and end your turn.") {
		t.Errorf("done R1:\n%s", out)
	}

	s = attached(t)
	if out := s.must("", "done", "P0"); strings.Contains(out, "without the human") {
		t.Errorf("no decisions:\n%s", out)
	}
}
