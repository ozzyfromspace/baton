package hooks

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// relayProposal is the agent on P2's timed proposal, queued for baton to put to the human.
var relayProposal = state.Relay{Kind: state.RelayProposal, Decision: "P2.d1", What: "use a fake clock", Because: "the real one drifts"}

// queue queues r, a question of the agent on P2, in the lead's run.
func queue(lead *fixture, r state.Relay) {
	lead.t.Helper()
	if r.Phase == "" {
		r.Phase = "P2"
	}
	lead.set(func(st *state.State) { state.QueueRelay(st, r, lead.now) })
}

// issue queues r and issues it to the mod as the coordinator does, with its deadline five minutes on.
func issue(lead *fixture, r state.Relay) {
	lead.t.Helper()
	queue(lead, r)
	lead.set(func(st *state.State) {
		q := st.FrontRelay()
		deadline := lead.now.Add(5 * time.Minute)
		state.IssueRelay(q, decide.RelayQuestion(*q, deadline).Text, 4, lead.inst, deadline, lead.now)
	})
}

// modAskOf is the mod's question call for the lead's front issued question, exactly as baton issued it.
func modAskOf(lead *fixture) map[string]any {
	lead.t.Helper()
	st := lead.state()
	r := st.RelayIssued()
	if r == nil {
		lead.t.Fatal("no agent's question was issued")
	}
	var labels []string
	for _, o := range decide.RelayOptions(*r) {
		labels = append(labels, o.Label)
	}
	in := ask(r.Question, labels...)
	in["tool_use_id"] = "toolu_plugin_0a1b"
	return in
}

// No phase agent's question ever reaches the lead's model: not with its tool calls, its stop or its
// prompts. Only baton's mod asks it, and the hooks issue nothing.
func TestTheLeadsModelIsNeverGivenAnAgentsQuestion(t *testing.T) {
	_, lead, _ := newWorker(t)
	queue(lead, relayProposal)
	for _, c := range []struct {
		event string
		in    map[string]any
		text  func(map[string]any) string
	}{
		{"PostToolUse", bash("go test ./..."), feedback},
		{"Stop", map[string]any{}, feedback},
		{"UserPromptSubmit", map[string]any{"prompt": "[baton] go on"}, additionalContext},
		{"UserPromptSubmit", map[string]any{"prompt": "how is it going?"}, additionalContext},
	} {
		out := lead.fire(c.event, c.in)
		if text := c.text(out) + say(out); strings.Contains(text, "P2") {
			t.Errorf("%s: told %q", c.event, text)
		}
	}
	if d := denyReason(lead.fire("PreToolUse", bash("go build ./..."))); d != "" {
		t.Errorf("the lead's call was held: %q", d)
	}
	if st := lead.state(); st.RelayIssued() != nil || st.FrontRelay() == nil {
		t.Errorf("relay %+v: a hook issued it", st.Relay)
	}
}

// The mod's question gets through whatever holds the lead's own calls, and is no call of the lead's
// (a landing does not wait for it); a question passed off as baton's is refused as the model's would be.
func TestTheModsQuestionGetsThrough(t *testing.T) {
	_, lead, _ := newWorker(t)
	issue(lead, relayProposal)
	lead.set(func(st *state.State) { st.BoundaryOwed = true })
	if d := denyReason(lead.fire("PreToolUse", bash("ls"))); d == "" {
		t.Fatal("a boundary is owed, yet the lead's call ran")
	}
	if d := denyReason(lead.fire("PreToolUse", modAskOf(lead))); d != "" {
		t.Errorf("the mod's question was refused: %q", d)
	}
	if st := lead.state(); len(st.Run.Tools) != 0 {
		t.Errorf("tools %+v: the mod's question counted as a call of the lead's", st.Run.Tools)
	}
	other := ask("baton: lunch?", "Yes", "No")
	other["tool_use_id"] = "toolu_plugin_ffff"
	if d := denyReason(lead.fire("PreToolUse", other)); d == "" {
		t.Error("a mod's question baton did not issue got through")
	}
}

// The question's dialog opening is the question asked: the human is pushed about a proposal, whose
// question names its deadline.
func TestAnAgentsQuestionAsked(t *testing.T) {
	_, lead, _ := newWorker(t)
	issue(lead, relayProposal)
	lead.now = lead.now.Add(3 * time.Second)
	lead.fire("PermissionRequest", modAskOf(lead))
	st := lead.state()
	if r := st.RelayIssued(); r == nil || !r.Asked || !r.AskedAt.Equal(lead.now) {
		t.Fatalf("not asked: %+v", st.Relay)
	}
	if d, _ := st.Run.Dialogs.Front(); d.Kind != "relay" {
		t.Errorf("the dialog is %q, not baton's own", d.Kind)
	}
	if got := noticeKinds(st); len(got) != 1 || got[0] != "proposal" || !strings.HasPrefix(st.Run.Notices[0].Text, "the agent on P2: the real one drifts") {
		t.Errorf("notices %+v", st.Run.Notices)
	}
	if !lead.logged("relay_asked", map[string]any{"phase": "P2", "decision": "P2.d1"}) {
		t.Errorf("events:\n%s", lead.eventLog())
	}
}

// The human's answer closes the mod's dialog, and reaches no model: the mod reports it, and the host
// applies it. A result due meanwhile waits for the lead's own next call.
func TestTheModsQuestionAnsweredReachesNoModel(t *testing.T) {
	_, lead, _ := newWorker(t)
	issue(lead, relayProposal)
	q := modAskOf(lead)
	lead.fire("PermissionRequest", q)
	lead.finished("P1", state.JobFailed)
	done := answer(q, "Go ahead")
	done["tool_use_id"] = q["tool_use_id"]
	if out := lead.fire("PostToolUse", done); len(out) != 0 {
		t.Errorf("the mod's question's result said %v", out)
	}
	st := lead.state()
	if st.Run.Dialogs.AnyOpen() {
		t.Errorf("dialogs %+v: the question's is still open", st.Run.Dialogs)
	}
	if r := st.RelayIssued(); r == nil || r.Answered != nil {
		t.Errorf("relay %+v: the hook answered it, not the mod's report", st.Relay)
	}
	if out := lead.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "Job j1") {
		t.Errorf("the result after the question: %v", out)
	}
}

// How long a question of baton's may wait before the mod answers it, and with what: the deadline of a
// timed proposal, a minute on screen at least, the context question's timeout, a block once the host's
// loop found nobody answered it in time (asked again until then); nothing for a question only the human
// answers, or for none of baton's.
func TestBound(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	q := decide.ProposalQuestion("gpg signing times out", "commit P0 unsigned", t0.Add(5*time.Minute))
	timed := func(st *state.State) {
		state.AddProposal(st, state.Decision{What: "commit P0 unsigned", Because: "gpg signing times out", Undo: "x", Deadline: t0.Add(5 * time.Minute), Question: q.Text}, t0)
	}
	outward := func(st *state.State) {
		state.AddProposal(st, state.Decision{What: "push the branch", Because: "b", Undo: "x", Outward: "push", Deadline: t0.Add(5 * time.Minute), Question: q.Text}, t0)
	}
	front := func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(q.Text), Since: t0, Kind: "proposal", Front: t0})
	}
	behind := func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Since: t0})
		front(st)
	}
	relay := func(st *state.State) {
		state.QueueRelay(st, state.Relay{Phase: "P2", Kind: state.RelayBlocked, Since: t0, Because: "need a key"}, t0)
		state.IssueRelay(st.FrontRelay(), "baton: the agent on P2 is blocked: need a key.", 4, "inst", t0.Add(5*time.Minute), t0)
	}
	rq := "baton: the agent on P2 is blocked: need a key."
	eq := "baton: the model stopped 4 times on P0 without reporting status"
	escalation := func(kind string) func(*state.State) {
		return func(st *state.State) {
			st.Run.Escalation = &state.Escalation{Kind: kind, Reason: "r", Since: t0, Question: eq, Asked: true}
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(eq), Since: t0, Kind: "escalation", Front: t0})
		}
	}
	// blocked is the block the escalation asks about, which the host's loop found lapsed after lapsed (0:
	// not yet).
	blocked := func(lapsed time.Duration) func(*state.State) {
		return func(st *state.State) {
			st.Blocked = &state.Block{Reason: "need a key", Since: t0}
			if lapsed > 0 {
				st.Blocked.Lapsed = t0.Add(lapsed)
			}
		}
	}
	pq := "baton: you approved a plan. Which?"
	planQuestion := func(same bool) func(*state.State) {
		return func(st *state.State) {
			st.PlanQuestion = &state.PlanQuestion{File: "plan.md", Same: same, Question: pq, Since: t0}
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(pq), Since: t0, Kind: "plan_question", Front: t0})
		}
	}
	for _, c := range []struct {
		name     string
		set      []func(*state.State)
		question string
		asked    time.Duration // after t0
		now      time.Duration // after t0
		want     Wait
	}{
		{"a timed proposal before its deadline", []func(*state.State){timed, front}, q.Text, 0, time.Minute, Wait{For: 30 * time.Second, Answer: "Go ahead"}},
		{"near its deadline", []func(*state.State){timed, front}, q.Text, 0, 4*time.Minute + 50*time.Second, Wait{For: 10 * time.Second, Answer: "Go ahead"}},
		{"at its deadline", []func(*state.State){timed, front}, q.Text, 0, 5 * time.Minute, Wait{Answer: "Go ahead"}},
		{"at its deadline, behind another dialog", []func(*state.State){timed, behind}, q.Text, 0, 5 * time.Minute, Wait{For: 30 * time.Second, Answer: "Go ahead"}},
		{"not on record yet", []func(*state.State){timed}, q.Text, 0, 5 * time.Minute, Wait{For: 30 * time.Second, Answer: "Go ahead"}},
		{"an outward proposal before its deadline", []func(*state.State){outward, front}, q.Text, 0, time.Minute, Wait{For: 30 * time.Second, Answer: "Not now"}},
		{"an outward proposal at its deadline", []func(*state.State){outward, front}, q.Text, 0, 5 * time.Minute, Wait{Answer: "Not now"}},
		{"a proposal with no deadline on record", []func(*state.State){func(st *state.State) {
			state.AddProposal(st, state.Decision{What: "push", Because: "b", Undo: "x", Question: q.Text}, t0)
		}, front}, q.Text, 0, time.Hour, Wait{}},
		{"the context question", []func(*state.State){func(st *state.State) { st.Run.WarnQuestion = "baton: the context holds a lot. Checkpoint now?" }, func(st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize("baton: the context holds a lot. Checkpoint now?"), Since: t0, Front: t0})
		}}, "baton: the context holds a lot. Checkpoint now?", 0, 20 * time.Minute, Wait{Answer: "Keep going"}},
		{"an agent's question at its deadline", []func(*state.State){relay}, rq, time.Minute, 5 * time.Minute, Wait{Answer: "Look again"}},
		{"an agent's question asked late", []func(*state.State){relay}, rq, 4*time.Minute + 30*time.Second, 5 * time.Minute, Wait{For: 30 * time.Second, Answer: "Look again"}},
		{"an agent's question settled meanwhile", []func(*state.State){relay, func(st *state.State) {
			state.AnswerRelay(st.FrontRelay(), state.ByWithdrawn, "", t0)
		}}, rq, 0, time.Minute, Wait{Answer: "Look again"}},
		{"a stall's question before its deadline", []func(*state.State){escalation("stalled")}, eq, 0, 9*time.Minute + 50*time.Second, Wait{For: 10 * time.Second, Answer: "Continue"}},
		{"a stall's question at its deadline", []func(*state.State){escalation("stalled")}, eq, 0, 10 * time.Minute, Wait{Answer: "Continue"}},
		{"a block's question, not lapsed: asked again", []func(*state.State){escalation("blocked"), blocked(0)}, eq, 0, time.Hour, Wait{For: 30 * time.Second, Answer: "Look again"}},
		{"a block's question the loop found lapsed", []func(*state.State){escalation("blocked"), blocked(30 * time.Minute)}, eq, 0, 30*time.Minute + time.Second, Wait{Answer: "Look again"}},
		{"lapsed, but only just on screen", []func(*state.State){escalation("blocked"), blocked(30 * time.Minute), func(st *state.State) {
			st.Run.Dialogs[0].Front = t0.Add(30 * time.Minute)
		}}, eq, 0, 30*time.Minute + 20*time.Second, Wait{For: 30 * time.Second, Answer: "Look again"}},
		{"lapsed, behind another dialog", []func(*state.State){func(st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Since: t0})
		}, escalation("blocked"), blocked(30 * time.Minute)}, eq, 0, time.Hour, Wait{For: 30 * time.Second, Answer: "Look again"}},
		{"lapsed before the human wrote", []func(*state.State){escalation("blocked"), blocked(30 * time.Minute), func(st *state.State) {
			st.Run.HumanAt = t0.Add(40 * time.Minute)
		}}, eq, 0, time.Hour, Wait{For: 30 * time.Second, Answer: "Look again"}},
		{"lapsed, the plan paused", []func(*state.State){escalation("blocked"), blocked(30 * time.Minute), func(st *state.State) { st.Mode = state.ModePaused }},
			eq, 0, time.Hour, Wait{}},
		{"a revised plan", []func(*state.State){planQuestion(true)}, pq, 0, 10 * time.Minute, Wait{Answer: "Continue with the revised plan"}},
		{"another plan", []func(*state.State){planQuestion(false)}, pq, 0, 10 * time.Minute, Wait{Answer: "Keep the current run"}},
		{"none of baton's", []func(*state.State){timed, front}, "baton: lunch?", 0, time.Hour, Wait{}},
		{"the plan paused", []func(*state.State){timed, front, func(st *state.State) { st.Mode = state.ModePaused }}, q.Text, 0, time.Hour, Wait{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.New()
			st.Mode, st.Current = state.ModeRunning, "P0"
			for _, set := range c.set {
				set(&st)
			}
			if got := bound(&st, one(c.question), t0.Add(c.asked), t0.Add(c.now), 20*time.Minute, 10*time.Minute); !reflect.DeepEqual(got, c.want) {
				t.Errorf("waits %+v, want %+v", got, c.want)
			}
		})
	}

	// In front, a question's clock runs from when baton first saw it there.
	st := state.New()
	st.Mode, st.Current = state.ModeRunning, "P0"
	timed(&st)
	st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Since: t0})
	st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(q.Text), Since: t0, Kind: "proposal"})
	bound(&st, one(q.Text), t0, t0.Add(6*time.Minute), 20*time.Minute, 10*time.Minute)
	st.Run.Dialogs.CloseExact("a1", "Bash", "k")
	if w := bound(&st, one(q.Text), t0, t0.Add(7*time.Minute), 20*time.Minute, 10*time.Minute); w.For != 30*time.Second {
		t.Errorf("first in front past its deadline: waits %+v, want a minute on screen", w)
	}
	if d, _ := st.Run.Dialogs.Front(); !d.Front.Equal(t0.Add(7 * time.Minute)) {
		t.Errorf("front since %s", d.Front)
	}
	if w := bound(&st, one(q.Text), t0, t0.Add(8*time.Minute), 20*time.Minute, 10*time.Minute); !reflect.DeepEqual(w, Wait{Answer: "Go ahead"}) {
		t.Errorf("a minute in front: waits %+v", w)
	}
}

// one is a call that asks question alone, with no options: how a question of baton's is asked about.
func one(question string) []bridge.Asked { return []bridge.Asked{{Question: question}} }

// A call of the lead model's own waits for the human escalation_timeout from when it was first in front,
// a minute at least, and then gets an answer to each of its questions: the option the model marked
// (Recommended), else its first; a stale question of baton's shape its third, where baton's default sits.
// Only while the plan runs, and only for a call whose every question offers something to choose.
func TestBoundOwn(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	db := bridge.Asked{Question: "Which database?", Options: []string{"sqlite", "Postgres (Recommended)"}}
	color := bridge.Asked{Question: "Which colour?", Options: []string{"red", "blue"}}
	checks := bridge.Asked{Question: "Which  checks?", Options: []string{"unit", "e2e"}, Multi: true}
	stale := bridge.Asked{Question: "baton: the model stopped 4 times on P0 without reporting status", Options: []string{"Pause baton", "Wait for me", "Continue"}}
	bare := bridge.Asked{Question: "Anything else?"}
	front := func(call ...bridge.Asked) func(*state.State) {
		return func(st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: callKey(call), Since: t0, Front: t0})
		}
	}
	behind := func(call ...bridge.Asked) func(*state.State) {
		return func(st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Since: t0})
			front(call...)(st)
		}
	}
	for _, c := range []struct {
		name string
		set  []func(*state.State)
		call []bridge.Asked
		esc  time.Duration
		now  time.Duration // after t0
		want Wait
	}{
		{"its recommended option, at the timeout", []func(*state.State){front(db)}, []bridge.Asked{db}, 10 * time.Minute, 10 * time.Minute,
			Wait{Answers: map[string]string{"Which database?": "Postgres (Recommended)"}}},
		{"its first, with none recommended", []func(*state.State){front(color)}, []bridge.Asked{color}, 10 * time.Minute, 10 * time.Minute,
			Wait{Answers: map[string]string{"Which colour?": "red"}}},
		{"before the timeout", []func(*state.State){front(color)}, []bridge.Asked{color}, 10 * time.Minute, 9*time.Minute + 50*time.Second,
			Wait{For: 10 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"long before it, asked again within the recheck", []func(*state.State){front(color)}, []bridge.Asked{color}, 10 * time.Minute, time.Minute,
			Wait{For: 30 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"several questions, a multi-select one too, each answered", []func(*state.State){front(db, color, checks)}, []bridge.Asked{db, color, checks}, 10 * time.Minute, 10 * time.Minute,
			Wait{Answers: map[string]string{"Which database?": "Postgres (Recommended)", "Which colour?": "red", "Which  checks?": "unit"}}},
		{"behind another dialog", []func(*state.State){behind(color)}, []bridge.Asked{color}, 10 * time.Minute, time.Hour,
			Wait{For: 30 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"not on record yet", nil, []bridge.Asked{color}, 10 * time.Minute, time.Hour,
			Wait{For: 30 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"another call in front", []func(*state.State){front(db)}, []bridge.Asked{color}, 10 * time.Minute, time.Hour,
			Wait{For: 30 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"a minute on screen at least", []func(*state.State){front(color)}, []bridge.Asked{color}, 10 * time.Second, 50 * time.Second,
			Wait{For: 10 * time.Second, Answers: map[string]string{"Which colour?": "red"}}},
		{"a stale question of baton's gets its default", []func(*state.State){front(stale)}, []bridge.Asked{stale}, 10 * time.Minute, 10 * time.Minute,
			Wait{Answers: map[string]string{stale.Question: "Continue"}}},
		{"a question with nothing to choose", []func(*state.State){front(color, bare)}, []bridge.Asked{color, bare}, 10 * time.Minute, time.Hour, Wait{}},
		{"the plan paused", []func(*state.State){front(db), func(st *state.State) { st.Mode = state.ModePaused }}, []bridge.Asked{db}, 10 * time.Minute, time.Hour, Wait{}},
		{"no plan", []func(*state.State){front(db), func(st *state.State) { st.Mode = state.ModeIdle }}, []bridge.Asked{db}, 10 * time.Minute, time.Hour, Wait{}},
		{"no question", nil, nil, 10 * time.Minute, time.Hour, Wait{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := state.New()
			st.Mode, st.Current = state.ModeRunning, "P0"
			for _, set := range c.set {
				set(&st)
			}
			if got := bound(&st, c.call, t0, t0.Add(c.now), 20*time.Minute, c.esc); !reflect.DeepEqual(got, c.want) {
				t.Errorf("waits %+v, want %+v", got, c.want)
			}
		})
	}

	// A call baton issued word for word is baton's, a call with one of its questions among others is not.
	st := state.New()
	st.Mode, st.Current = state.ModeRunning, "P0"
	q := decide.ProposalQuestion("gpg signing times out", "commit P0 unsigned", t0.Add(5*time.Minute))
	state.AddProposal(&st, state.Decision{What: "commit P0 unsigned", Because: "b", Undo: "x", Deadline: t0.Add(5 * time.Minute), Question: q.Text}, t0)
	mixed := []bridge.Asked{{Question: q.Text, Options: []string{"Wait for me", "Pause baton", "Go ahead"}}, color}
	front(mixed...)(&st)
	if w := bound(&st, mixed, t0, t0.Add(10*time.Minute), 20*time.Minute, 10*time.Minute); w.Answer != "" || w.Answers["Which colour?"] != "red" || w.Answers[q.Text] != "Go ahead" {
		t.Errorf("a call that only includes baton's question: %+v", w)
	}
}

// The mod's answer to a call of the lead model's own at its deadline closes its dialog, tells the human
// (and pushes) and the model that baton chose for them, and is logged by timeout; once.
func TestSettleOwn(t *testing.T) {
	f := newFixture(t, true)
	f.set(func(st *state.State) { st.Mode, st.Current = state.ModeRunning, "P0" })
	call := []bridge.Asked{{Question: "Which database?", Options: []string{"sqlite", "Postgres (Recommended)"}}, {Question: "Which colour?", Options: []string{"red", "blue"}}}
	in := map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "options": []any{map[string]any{"label": "sqlite"}, map[string]any{"label": "Postgres (Recommended)"}}},
		map[string]any{"question": "Which colour?", "options": []any{map[string]any{"label": "red"}, map[string]any{"label": "blue"}}},
	}}}
	f.fire("PermissionRequest", in)
	if st := f.state(); len(st.Run.Dialogs) != 1 || st.Run.Dialogs[0].Key != callKey(call) {
		t.Fatalf("dialogs %+v: the call's key differs from the hooks'", st.Run.Dialogs)
	}
	answers := map[string]string{"Which database?": "Postgres (Recommended)", "Which colour?": "red"}
	if err := SettleOwn(f.store, call, answers, f.now); err != nil {
		t.Fatal(err)
	}
	st := f.state()
	if st.Run.Dialogs.AnyOpen() || len(st.Run.Announce) == 0 || !strings.Contains(st.Run.Announce[len(st.Run.Announce)-1], `"Which colour?" → "red"`) {
		t.Errorf("dialogs %+v, announce %q", st.Run.Dialogs, st.Run.Announce)
	}
	if n := st.Run.Notices; len(n) == 0 || n[len(n)-1].Kind != "answered" {
		t.Errorf("notices %+v: no push", n)
	}
	if !f.logged("question_answered", map[string]any{"by": state.ByTimeout, "via": "mod", "questions": float64(2), "answer": "Postgres (Recommended)"}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "so baton chose for the human") {
		t.Errorf("told at the next call: %v", out)
	}
	if err := SettleOwn(f.store, call, answers, f.now); err != nil || len(f.state().Run.Announce) > len(st.Run.Announce) {
		t.Errorf("settled again: %v, announce %q", err, f.state().Run.Announce)
	}
}

// A question the mod answered at its deadline is recorded as its result would have been: a proposal of
// the lead's own goes ahead, by baton, and the model is told at its next call; the context question keeps
// going; an agent's question only closes its dialog (the mod's answered report settles it). Each once.
func TestSettle(t *testing.T) {
	f := newFixture(t, true)
	f.set(func(st *state.State) { st.Mode, st.Current = state.ModeRunning, "P0" })
	in := f.propose(false)
	f.fire("PermissionRequest", in)
	q := in["tool_input"].(map[string]any)["questions"].([]any)[0].(map[string]any)["question"].(string)
	own, err := Settle(f.store, q, "Go ahead", f.now, f.env(f.inst))
	if err != nil || !own {
		t.Fatalf("settled %v, %v", own, err)
	}
	st := f.state()
	if p := st.Decisions[len(st.Decisions)-1]; p.ID != "d1" || p.Resolved == nil || p.Resolved.By != state.ByTimeout {
		t.Fatalf("decisions %+v", st.Decisions)
	}
	if st.Run.Dialogs.AnyOpen() || len(st.Run.Announce) == 0 || !strings.Contains(st.Run.Announce[len(st.Run.Announce)-1], "nobody answered") {
		t.Errorf("dialogs %+v, announce %q", st.Run.Dialogs, st.Run.Announce)
	}
	if !f.logged("proposal_resolved", map[string]any{"id": "d1", "by": state.ByTimeout, "via": "mod"}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "Nobody answered your proposal d1") {
		t.Errorf("told at the next call: %v", out)
	}
	if own, err := Settle(f.store, q, "Go ahead", f.now, f.env(f.inst)); own || err != nil {
		t.Errorf("settled again: %v, %v", own, err)
	}

	f.set(func(st *state.State) { st.Run.WarnQuestion = "baton: the context holds a lot. Checkpoint now?" })
	if own, _ := Settle(f.store, "baton: the context holds a lot. Checkpoint now?", "Keep going", f.now, f.env(f.inst)); !own || f.state().Run.WarnQuestion != "" {
		t.Errorf("the context question: own %v, %q", own, f.state().Run.WarnQuestion)
	}

	// A stall's question answered at its deadline lets the plan go on, as the human's Continue would.
	f.set(func(st *state.State) {
		st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "the model stopped 4 times on P0 without reporting status", Since: f.now,
			Question: "baton: the model stopped 4 times on P0 without reporting status", Asked: true}
	})
	if own, err := Settle(f.store, "baton: the model stopped 4 times on P0 without reporting status", "Continue", f.now, f.env(f.inst)); !own || err != nil {
		t.Fatalf("a stall's question: %v, %v", own, err)
	}
	if st := f.state(); st.Run.Escalation != nil || !f.logged("resumed", map[string]any{"by": "timeout", "via": "mod"}) || !st.Run.HumanAt.IsZero() {
		t.Errorf("escalation %+v, human at %s; events:\n%s", st.Run.Escalation, st.Run.HumanAt, f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "Nobody answered baton's question") {
		t.Errorf("told at the next call: %v", out)
	}
	// A plan approved mid-run that nobody answered about: the current run is kept (another file), from the
	// host's loop, with no dialog on screen (Lapse).
	f.set(func(st *state.State) {
		st.PlanQuestion = &state.PlanQuestion{File: "/plans/other.md", Same: false, Question: "baton: you approved a new plan. Which?", Since: f.now}
	})
	if own, err := Lapse(f.store, "baton: you approved a new plan. Which?", "Keep the current run", f.now, f.env(f.inst)); !own || err != nil {
		t.Fatalf("a plan approved mid-run: %v, %v", own, err)
	}
	if st := f.state(); st.PlanQuestion != nil || !f.logged("plan_question_resolved", map[string]any{"answer": "Keep the current run", "by": "timeout", "via": "lapsed"}) {
		t.Errorf("plan question %+v; events:\n%s", st.PlanQuestion, f.eventLog())
	}

	_, lead, _ := newWorker(t)
	issue(lead, relayProposal)
	aq := modAskOf(lead)
	lead.fire("PermissionRequest", aq)
	st = lead.state()
	text := st.RelayIssued().Question
	if own, err := Settle(lead.store, text, "Go ahead", lead.now, lead.env(lead.inst)); !own || err != nil {
		t.Fatalf("an agent's question: %v, %v", own, err)
	}
	if st = lead.state(); st.Run.Dialogs.AnyOpen() || st.RelayIssued() == nil {
		t.Errorf("relay %+v, dialogs %+v", st.Relay, st.Run.Dialogs)
	}
}

// Every hook path of the mod's question fails open.
func TestTheRelayHooksFailOpen(t *testing.T) {
	for _, c := range []struct {
		name, event string
		in          func(lead *fixture) map[string]any
	}{
		{"asked", "PreToolUse", modAskOf},
		{"drawn", "PermissionRequest", modAskOf},
		{"answered", "PostToolUse", func(lead *fixture) map[string]any {
			q := modAskOf(lead)
			lead.fire("PermissionRequest", q)
			out := answer(q, "Go ahead")
			out["tool_use_id"] = q["tool_use_id"]
			return out
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead, _ := newWorker(t)
			issue(lead, relayProposal)
			in := c.in(lead)
			open := func(ctx Context) (*state.Store, error) { panic("boom") }
			in["hook_event_name"], in["session_id"] = c.event, lead.sid
			raw, _ := json.Marshal(in)
			var out, errb bytes.Buffer
			code := Dispatch(c.event, bytes.NewReader(raw), &out, &errb, lead.env(lead.inst), lead.clock, Handlers(Deps{Open: open}))
			if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
				t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
			}
		})
	}
}

// An outward proposal the mod answered at its deadline is answered Not now, by baton: the model is told to
// do it only if it is the safe kind, and it is left for the human otherwise.
func TestSettleAnOutwardProposal(t *testing.T) {
	f := newFixture(t, true)
	f.set(func(st *state.State) { st.Mode, st.Current = state.ModeRunning, "P0" })
	in := f.propose(true)
	f.fire("PermissionRequest", in)
	q := in["tool_input"].(map[string]any)["questions"].([]any)[0].(map[string]any)["question"].(string)
	if own, err := Settle(f.store, q, "Not now", f.now, f.env(f.inst)); !own || err != nil {
		t.Fatalf("settled %v, %v", own, err)
	}
	st := f.state()
	if p := st.Decisions[0]; p.Resolved == nil || p.Resolved.By != state.ByTimeout || p.Resolved.Answer != state.NotNow || !p.LeftForHuman() || p.WithoutHuman() {
		t.Fatalf("resolved %+v", p.Resolved)
	}
	if n := len(st.Run.Announce); n == 0 || !strings.Contains(st.Run.Announce[n-1], "so baton answered Not now for you on d1") || !st.Run.HumanAt.IsZero() {
		t.Errorf("announce %q, human at %s", st.Run.Announce, st.Run.HumanAt)
	}
	if !f.logged("proposal_resolved", map[string]any{"id": "d1", "by": state.ByTimeout, "answer": "Not now", "via": "mod"}) {
		t.Errorf("events:\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); !strings.Contains(feedback(out), "baton answered Not now for the human") {
		t.Errorf("told at the next call: %v", out)
	}
}
