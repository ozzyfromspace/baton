package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// relayProposal is the worker on P2's timed proposal, queued for the lead to put to the human.
var relayProposal = state.Relay{Kind: state.RelayProposal, Decision: "P2.d1", What: "use a fake clock", Because: "the real one drifts"}

// queue queues r, a question of the worker on P2, in the lead's run.
func queue(lead *fixture, r state.Relay) {
	lead.t.Helper()
	if r.Phase == "" {
		r.Phase = "P2"
	}
	lead.set(func(st *state.State) { state.QueueRelay(st, r, lead.now) })
}

// relayAsk is the question call for the lead's front relayed question, exactly as baton issued it.
func relayAsk(lead *fixture) map[string]any {
	lead.t.Helper()
	st := lead.state()
	r := st.RelayIssued()
	if r == nil {
		lead.t.Fatal("no worker's question was issued")
	}
	var labels []string
	for _, o := range decide.RelayOptions(*r) {
		labels = append(labels, o.Label)
	}
	return ask(r.Question, labels...)
}

// The lead is given a worker's question, word for word, through whichever of its channels comes first:
// the result of its next tool call, a refusal of its stop, or the prompt of the line the host types. Its
// deadline starts then.
func TestTheLeadIsGivenAWorkersQuestion(t *testing.T) {
	for _, c := range []struct {
		name, event string
		in          map[string]any
		text        func(out map[string]any) string
		say         bool // the human is told it comes
	}{
		{"mid-turn", "PostToolUse", bash("go test ./..."), feedback, true},
		{"at its stop", "Stop", map[string]any{}, feedback, true},
		{"with the host's line", "UserPromptSubmit", map[string]any{"prompt": decide.RelayTyped("P2")}, additionalContext, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead, _ := newWorker(t)
			queue(lead, relayProposal)
			out := lead.fire(c.event, c.in)
			st := lead.state()
			r := st.RelayIssued()
			if r == nil || !r.Issued.Equal(lead.now) || r.Asked {
				t.Fatalf("issued %+v", st.Relay)
			}
			deadline := decide.Deadline(lead.now, 5*time.Minute)
			if want := decide.RelayQuestion(*r, deadline); r.Question != want.Text || !r.Deadline.Equal(deadline) {
				t.Errorf("issued %q, deadline %s; want %q", r.Question, r.Deadline, want.Text)
			}
			text := c.text(out)
			for _, want := range []string{"[baton] ", "worker on P2", relayQuestion(*r).Call(), "baton gives the answer to the worker itself"} {
				if !strings.Contains(text, want) {
					t.Errorf("told %q, which lacks %q", text, want)
				}
			}
			if c.say && !strings.Contains(say(out), "baton: the worker on P2 has a question for you — Claude puts it to you now") {
				t.Errorf("announced %q", say(out))
			}
			via := map[string]string{"PostToolUse": "tool", "Stop": "stop", "UserPromptSubmit": "prompt"}[c.event]
			if !lead.logged("relay_issued", map[string]any{"phase": "P2", "type": "proposal", "decision": "P2.d1", "via": via, "timed": true}) {
				t.Errorf("events:\n%s", lead.eventLog())
			}

			// Until it is asked, nothing else of the lead's runs, and the call that asks it does.
			if d := denyReason(lead.fire("PreToolUse", bash("go build ./..."))); !strings.Contains(d, "Not yet: put the question of the worker on P2 to the human first") {
				t.Errorf("held with %q", d)
			}
			if d := denyReason(lead.fire("PreToolUse", relayAsk(lead))); d != "" {
				t.Errorf("the question itself was refused: %q", d)
			}
			// Given once mid-turn; issued once.
			if out := lead.fire("PostToolUse", bash("ls")); strings.Contains(feedback(out), "has a question") {
				t.Errorf("given again: %v", out)
			}
			if st := lead.state(); st.RelayIssued() == nil || !st.RelayIssued().Issued.Equal(lead.now) {
				t.Errorf("issued again: %+v", st.Relay)
			}
		})
	}
}

// A human's prompt is theirs: the question comes with the next tool call, or at the stop.
func TestAHumansPromptDoesNotCarryAWorkersQuestion(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	if out := lead.fire("UserPromptSubmit", map[string]any{"prompt": "how is it going?"}); strings.Contains(additionalContext(out), "has a question") {
		t.Fatalf("given with the human's prompt: %v", out)
	}
	if st := lead.state(); st.RelayIssued() != nil {
		t.Fatal("issued with the human's prompt")
	}
}

// One question at a time: a question of the lead's own goes first, and a second worker question waits
// until the first is answered.
func TestOneQuestionAtATime(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	queue(lead, state.Relay{Phase: "P2", Kind: state.RelayBlocked, Since: lead.now, Because: "need a key"})
	lead.propose(false) // the lead's own
	if out, st := lead.fire("PostToolUse", bash("ls")), lead.state(); strings.Contains(feedback(out), "has a question") || st.RelayIssued() != nil {
		t.Fatalf("a worker's question went before the lead's own: %v", out)
	}
	if out := lead.fire("Stop", map[string]any{}); !strings.Contains(feedback(out), "put your proposal d1 to the human first") {
		t.Fatalf("stop: %v", out)
	}
	lead.set(func(st *state.State) { state.Resolve(st, "d1", state.ByHuman, "Go ahead", lead.now) })

	lead.fire("PostToolUse", bash("ls"))
	st := lead.state()
	first := st.RelayIssued()
	if first == nil || first.Kind != state.RelayProposal {
		t.Fatalf("issued %+v", first)
	}
	lead.fire("PermissionRequest", relayAsk(lead))
	if out := lead.fire("PostToolUse", bash("ls")); strings.Contains(feedback(out), "has a question") {
		t.Fatalf("a second question while the first waits: %v", out)
	}
	lead.fire("PostToolUse", answer(relayAsk(lead), "Go ahead"))
	if out := lead.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "is blocked: need a key") {
		t.Fatalf("the next question: %v", out)
	}
}

// The question's dialog opening is the question asked: the human is pushed about a proposal, whose
// question names its deadline.
func TestAWorkersQuestionAsked(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	lead.fire("PostToolUse", bash("ls"))
	lead.fire("PermissionRequest", relayAsk(lead))
	st := lead.state()
	if r := st.RelayIssued(); r == nil || !r.Asked {
		t.Fatalf("not asked: %+v", st.Relay)
	}
	if d, _ := st.Run.Dialogs.Front(); d.Kind != "relay" {
		t.Errorf("the dialog is %q, not baton's own", d.Kind)
	}
	if got := noticeKinds(st); len(got) != 1 || got[0] != "proposal" || !strings.HasPrefix(st.Run.Notices[0].Text, "worker P2: the real one drifts") {
		t.Errorf("notices %+v", st.Run.Notices)
	}
	if !lead.logged("relay_asked", map[string]any{"phase": "P2", "decision": "P2.d1"}) {
		t.Errorf("events:\n%s", lead.eventLog())
	}
	if out := lead.fire("Stop", map[string]any{}); out["decision"] == "block" && strings.Contains(feedback(out), "worker on P2") {
		t.Errorf("asked, yet the stop was refused for it: %v", out)
	}
}

// The answer is found by the question's exact text and recorded for the coordinator to apply; the lead
// is told it has nothing to do about it, and the human what becomes of it.
func TestAWorkersQuestionAnswered(t *testing.T) {
	for _, c := range []struct {
		name   string
		r      state.Relay
		answer string
		auto   bool
		by     string
		say    string
		tell   string
		paused bool
	}{
		{"go ahead", relayProposal, "Go ahead", false, state.ByHuman, "baton: you chose Go ahead — the worker on P2 does it", "The human answered the question of the worker on P2", false},
		{"nobody answered", relayProposal, "Go ahead", true, state.ByTimeout, "baton: nobody answered by", "so baton chose Go ahead for the human", false},
		{"wait for me", relayProposal, "Wait for me", false, state.ByHuman, "baton: the worker on P2 waits for you — answer it in its session: baton view P2", "nothing about it is yours to do", false},
		{"pause", relayProposal, "Pause baton", false, state.ByHuman, "baton: paused — the human is driving", "baton is paused", true},
		{"own words", relayProposal, "use the staging clock", false, state.ByHuman, "baton: your answer went to the worker on P2", "The human answered", false},
		{"a block, continue", state.Relay{Kind: state.RelayBlocked, Since: time.Unix(1, 0), Because: "need a key"}, "Continue", false, state.ByHuman,
			"baton: the worker on P2 carries on", "The human answered", false},
		{"a block nobody answered", state.Relay{Kind: state.RelayBlocked, Since: time.Unix(1, 0), Because: "need a key"}, "Wait for me", true, state.ByTimeout,
			"baton: nobody answered, so the worker on P2 waits for you", "so baton chose Wait for me", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead, _ := newWorker(t)
			queue(lead, c.r)
			lead.fire("PostToolUse", bash("ls"))
			q := relayAsk(lead)
			lead.fire("PermissionRequest", q)
			if c.auto {
				lead.set(func(st *state.State) { st.Run.Dialogs[0].AutoAnswered = lead.now })
			}
			lead.now = lead.now.Add(time.Minute)
			out := lead.fire("PostToolUse", answer(q, c.answer))
			st := lead.state()
			r := st.Relay[0]
			if r.Answered == nil || r.Answered.By != c.by || r.Answered.Answer != c.answer || !r.Answered.At.Equal(lead.now) || !r.Applied.IsZero() {
				t.Fatalf("answered %+v", r.Answered)
			}
			if !strings.Contains(say(out), c.say) || !strings.Contains(feedback(out), c.tell) {
				t.Errorf("said %q, told %q", say(out), feedback(out))
			}
			if (st.Mode == state.ModePaused) != c.paused {
				t.Errorf("mode %s", st.Mode)
			}
			if humanAt := st.Run.HumanAt.Equal(lead.now); humanAt == c.auto {
				t.Errorf("the human took part: %v", humanAt)
			}
			if !lead.logged("relay_answered", map[string]any{"phase": "P2", "answer": c.answer, "by": c.by}) {
				t.Errorf("events:\n%s", lead.eventLog())
			}
			if len(st.Unapplied()) != 1 || st.RelayIssued() != nil {
				t.Errorf("unapplied %+v; still issued %+v", st.Unapplied(), st.RelayIssued())
			}
		})
	}
}

// A question settled meanwhile (in the worker's own session, or as the worker went) is left as it was,
// whatever the human answers in the lead.
func TestALateAnswerChangesNothing(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	lead.fire("PostToolUse", bash("ls"))
	q := relayAsk(lead)
	lead.set(func(st *state.State) { state.AnswerRelay(st.FrontRelay(), state.ByWithdrawn, "", lead.now) })
	out := lead.fire("PostToolUse", answer(q, "Go ahead"))
	if r := lead.state().Relay[0]; r.Answered.By != state.ByWithdrawn {
		t.Errorf("answered %+v", r.Answered)
	}
	if !strings.Contains(feedback(out), "was settled already") || !strings.Contains(say(out), "your answer changed nothing") {
		t.Errorf("out %v", out)
	}
}

// A question whose dialog went away unanswered waits for the human in the worker's own session.
func TestAWorkersQuestionThatWentAway(t *testing.T) {
	for _, event := range []string{"PostToolUseFailure", "Stop", "UserPromptSubmit"} {
		t.Run(event, func(t *testing.T) {
			_, lead, _ := newWorker(t)
			queue(lead, relayProposal)
			lead.fire("PostToolUse", bash("ls"))
			q := relayAsk(lead)
			lead.fire("PermissionRequest", q)
			in := map[string]any{}
			switch event {
			case "PostToolUseFailure":
				in = q
			case "UserPromptSubmit":
				lead.set(func(st *state.State) { st.Run.Dialogs = nil }) // Esc
				in = map[string]any{"prompt": "never mind"}
			default:
				lead.set(func(st *state.State) { st.Run.Dialogs = nil })
			}
			lead.fire(event, in)
			if r := lead.state().Relay[0]; r.Answered == nil || r.Answered.By != state.ByDeclined {
				t.Fatalf("answered %+v", r.Answered)
			}
			if !lead.logged("relay_answered", map[string]any{"phase": "P2", "by": state.ByDeclined}) {
				t.Errorf("events:\n%s", lead.eventLog())
			}
			// The human is told at the lead's next hook.
			if out := lead.fire("PostToolUse", bash("ls")); !strings.Contains(say(out), "the question of the worker on P2 went unanswered — it waits for you in its session: baton view P2") {
				t.Errorf("announced %q", say(out))
			}
		})
	}
}

// A lead that will not put a worker's question to the human is asked MaxStopBlocks times: then the
// question waits for the human in the worker's session, they are pushed about it, and the lead's run
// goes on.
func TestALeadThatWillNotAskIt(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	for i := range MaxStopBlocks {
		if out := lead.fire("Stop", map[string]any{}); !strings.Contains(feedback(out), "You stopped, but put the question of the worker on P2 to the human first") {
			t.Fatalf("stop %d: %v", i+1, out)
		}
	}
	out := lead.fire("Stop", map[string]any{})
	if strings.Contains(feedback(out), "worker on P2") || !strings.Contains(say(out), "answer it in its session: baton view P2") {
		t.Fatalf("stop %d: %v", MaxStopBlocks+1, out)
	}
	st := lead.state()
	if r := st.Relay[0]; r.Answered == nil || r.Answered.By != state.ByDeclined || st.Run.Escalation != nil {
		t.Errorf("answered %+v, escalation %+v", r.Answered, st.Run.Escalation)
	}
	if got := noticeKinds(st); len(got) != 1 || got[0] != "relay" {
		t.Errorf("notices %v", got)
	}
}

// A worker whose block or review waits for the human goes on once they write in its own session: their
// message is the answer.
func TestAWorkerAnsweredInItsOwnSession(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(st *state.State, now time.Time)
		tell string
	}{
		{"blocked", func(st *state.State, now time.Time) { state.SetBlocked(st, "need a key", now) }, "their message answers your block (need a key)"},
		{"a review", func(st *state.State, now time.Time) { state.DueReview(st, "P2", now) }, "their message answers the review of the decisions P2 made without them"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, _ := newWorker(t)
			w.fire("SessionStart", map[string]any{"source": "startup"})
			w.set(func(st *state.State) { c.set(st, w.now) })
			w.fire("Stop", map[string]any{})
			w.now = w.now.Add(time.Minute)
			out := w.fire("UserPromptSubmit", map[string]any{"prompt": "use the spare key in the vault"})
			if !strings.Contains(additionalContext(out), c.tell) {
				t.Errorf("told %q", additionalContext(out))
			}
			if st := w.state(); st.Blocked != nil || st.ReviewDue != "" || st.Run.Escalation != nil || st.Mode != state.ModeRunning {
				t.Errorf("still waiting: blocked %+v, review %q, escalation %+v", st.Blocked, st.ReviewDue, st.Run.Escalation)
			}
			if !w.logged("resumed", map[string]any{"in": "worker session"}) {
				t.Errorf("events:\n%s", w.eventLog())
			}
		})
	}
}

// Every new path of the hooks around a worker's question fails open: a panic in one exits 0 and writes
// nothing. (A call held until the question is asked changes nothing and logs nothing, inside a handler
// whose panics TestPanicFailsOpenForEveryEvent covers.)
func TestTheRelayHooksFailOpen(t *testing.T) {
	for _, c := range []struct {
		name, event string
		in          func(lead *fixture) map[string]any
		worker      bool
	}{
		{"given mid-turn", "PostToolUse", func(*fixture) map[string]any { return bash("ls") }, false},
		{"given at the stop", "Stop", func(*fixture) map[string]any { return map[string]any{} }, false},
		{"given with the host's line", "UserPromptSubmit", func(*fixture) map[string]any { return map[string]any{"prompt": decide.RelayTyped("P2")} }, false},
		{"asked", "PermissionRequest", func(lead *fixture) map[string]any { lead.fire("PostToolUse", bash("ls")); return relayAsk(lead) }, false},
		{"answered", "PostToolUse", func(lead *fixture) map[string]any {
			lead.fire("PostToolUse", bash("ls"))
			q := relayAsk(lead)
			lead.fire("PermissionRequest", q)
			return answer(q, "Go ahead")
		}, false},
		{"answered in the worker's session", "UserPromptSubmit", func(*fixture) map[string]any { return map[string]any{"prompt": "go on"} }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead, _ := newWorker(t)
			queue(lead, relayProposal)
			f := lead
			if c.worker {
				f = w
				w.set(func(st *state.State) { state.SetBlocked(st, "need a key", w.now) })
			}
			in := c.in(f)
			open := func(ctx Context) (*state.Store, error) {
				s, err := OpenRun(f.clock)(ctx)
				if s != nil {
					s.Now = func() time.Time { panic("boom") }
				}
				return s, err
			}
			in["hook_event_name"], in["session_id"] = c.event, f.sid
			raw, _ := json.Marshal(in)
			var out, errb bytes.Buffer
			code := Dispatch(c.event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
			if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
				t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
			}
		})
	}
}
