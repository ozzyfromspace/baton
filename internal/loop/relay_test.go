package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// relayed is the worker on P3's timed proposal, as its sub-run queues it.
var relayed = state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "use a fake clock", Because: "the real one drifts"}

// An idle lead is woken to put a worker's question to the human: baton types its line, waits for the
// prompt, types it once more at most, and never while the lead has a question of its own.
func TestTheIdleLeadIsWokenForAWorkersQuestion(t *testing.T) {
	r := newIdleRig(t)
	r.set(func(st *state.State) { state.QueueRelay(st, relayed, r.now) })
	r.tick(5 * time.Second)
	if len(r.in.text) != 1 || r.in.text[0] != decide.RelayTyped("P3") {
		t.Fatalf("typed %q", r.in.text)
	}
	if !strings.Contains(r.events(), `"kind":"relay_typed"`) || !strings.Contains(r.events(), `"decision":"P3.d1"`) {
		t.Errorf("events:\n%s", r.events())
	}
	r.run(30 * time.Second)
	if len(r.in.text) != 1 {
		t.Fatalf("typed again before its prompt could arrive: %q", r.in.text)
	}
	r.run(10 * time.Minute)
	if n := strings.Count(strings.Join(r.in.text, "\n"), decide.RelayTyped("P3")); n != MaxRelayLines {
		t.Fatalf("typed %d times: %q", n, r.in.text)
	}

	// Asked: nothing more to type. A question of the lead's own: it waits.
	for name, set := range map[string]func(st *state.State, now time.Time){
		"asked": func(st *state.State, now time.Time) {
			state.IssueRelay(st.FrontRelay(), "baton: worker P3: q", now.Add(5*time.Minute), now)
			st.FrontRelay().Asked = true
		},
		"the lead's own proposal": func(st *state.State, now time.Time) { state.AddProposal(st, state.Decision{What: "x"}, now) },
		"paused":                  func(st *state.State, _ time.Time) { st.Mode = state.ModePaused },
	} {
		t.Run(name, func(t *testing.T) {
			r := newIdleRig(t)
			r.set(func(st *state.State) {
				state.QueueRelay(st, relayed, r.now)
				set(st, r.now)
			})
			r.run(time.Minute)
			for _, s := range r.in.text {
				if s == decide.RelayTyped("P3") {
					t.Fatalf("typed %q", r.in.text)
				}
			}
		})
	}
}

// A lead whose last turn failed in a way baton cannot retry asks nothing until the human acts: the
// workers' questions are left for the human in each worker's own session, at once, and so are those
// queued after. An error baton retries leaves them queued for the lead.
func TestALeadThatStoppedLeavesWorkersQuestions(t *testing.T) {
	second := state.Relay{Phase: "P4", Kind: state.RelayBlocked, Because: "need a key", Since: time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)}
	for _, c := range []struct {
		err  string
		left bool
	}{{"invalid_request", true}, {"authentication_failed", true}, {state.ErrRefusal, true}, {"overloaded", false}} {
		t.Run(c.err, func(t *testing.T) {
			r := newIdleRig(t)
			r.set(func(st *state.State) {
				state.QueueRelay(st, relayed, r.now)
				st.Run.LastError = &state.StopError{Error: c.err, At: r.now}
			})
			r.tick(5 * time.Second)
			r.set(func(st *state.State) { state.QueueRelay(st, second, r.now) })
			r.tick(5 * time.Second)
			for _, q := range r.state().Relay {
				if left := q.Answered != nil && q.Answered.By == state.ByLeadStopped; left != c.left {
					t.Errorf("%s's question left for the human: %v, want %v (%+v)", q.Phase, left, c.left, q.Answered)
				}
			}
			if n := strings.Count(r.events(), `"by":"lead_stopped"`); c.left && n != 2 || !c.left && n != 0 {
				t.Errorf("%d relay_answered events:\n%s", n, r.events())
			}
			for _, s := range r.in.text {
				if s == decide.RelayTyped("P3") {
					t.Fatalf("typed %q", r.in.text)
				}
			}
		})
	}
}

// issuedRig is an idle lead with worker question q given to it and asked: its dialog is the only one
// open.
func issuedRig(t *testing.T, q state.Relay) (*rig, state.Relay) {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		state.QueueRelay(st, q, r.now)
		f := st.FrontRelay()
		deadline := r.now.Add(DefaultTiming.EscalationTimeout)
		if f.Timed() {
			deadline = decide.Deadline(r.now, DefaultTiming.EscalationTimeout)
		}
		state.IssueRelay(f, decide.RelayQuestion(*f, deadline).Text, deadline, r.now)
		f.Asked = true
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(f.Question), Since: r.now, Kind: "relay"})
	})
	st := r.state()
	return r, *st.FrontRelay()
}

// A worker's question nobody answers is answered by baton at its deadline, with its third option, and
// nobody is pushed about the dialog meanwhile: the lead never waits on a worker's question.
func TestAWorkersQuestionNobodyAnswers(t *testing.T) {
	for _, q := range []state.Relay{relayed, {Phase: "P3", Kind: state.RelayBlocked, Since: time.Unix(1, 0), Because: "need a key"},
		{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "push the tag", Because: "x", Untimed: "it looks outward-facing"}} {
		t.Run(q.Kind+q.Untimed, func(t *testing.T) {
			r, f := issuedRig(t, q)
			r.view.Draft = true // keys go to the dialog: a draft does not matter
			r.runTo(f.Deadline.Add(-10 * time.Second))
			if len(r.in.text) != 0 {
				t.Fatalf("answered before its deadline: %q", r.in.text)
			}
			r.run(20 * time.Second)
			if len(r.in.text) != 1 || r.in.text[0] != "3" {
				t.Fatalf("typed %q", r.in.text)
			}
			if !strings.Contains(r.events(), `"dialog":"relay"`) || !strings.Contains(r.events(), `"phase":"P3"`) {
				t.Errorf("events:\n%s", r.events())
			}
			r.run(time.Hour)
			if len(r.in.text) != 1 || len(r.push.kinds) != 0 {
				t.Errorf("typed %q, pushed %v", r.in.text, r.push.kinds)
			}
		})
	}
}

// subRun is a worker's sub-run on P3, under way, with the question q asks about waiting in it.
func subRun(t *testing.T, q state.Relay, now time.Time) state.State {
	t.Helper()
	st := state.AttachWorker(state.New(), "P3")
	state.Start(&st, now, state.Origin{})
	switch q.Kind {
	case state.RelayProposal:
		if _, err := state.AddProposal(&st, state.Decision{What: q.What, Because: q.Because, Undo: "use the real clock", Untimed: q.Untimed, Relayed: true}, now); err != nil {
			t.Fatal(err)
		}
	case state.RelayBlocked:
		state.SetBlocked(&st, q.Because, q.Since)
		st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "blocked on P3: " + q.Because, Since: q.Since, Asked: true}
	case state.RelayReview:
		state.NoteOn(&st, "P3", "used a fake", "", now)
		state.DueReview(&st, "P3", q.Since)
		st.Run.Escalation = &state.Escalation{Kind: "review", Reason: "review", Since: q.Since, Asked: true}
	}
	return st
}

// The answer to a worker's question is applied to its sub-run as its own answer would have been, and the
// worker is given it like a job result.
func TestAnAnswerIsAppliedToTheWorker(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	block := state.Relay{Phase: "P3", Kind: state.RelayBlocked, Since: since, Because: "need a key"}
	review := state.Relay{Phase: "P3", Kind: state.RelayReview, Since: since, Count: 1}
	for _, c := range []struct {
		name    string
		q       state.Relay
		answer  string
		by      string
		maxAuto int
		result  string
		check   func(t *testing.T, st state.State)
		tell    string // in the message the worker is given ("" for none)
	}{
		{"go ahead", relayed, "Go ahead", state.ByHuman, 0, "goes ahead", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByHuman || d.WithoutHuman() {
				t.Errorf("decision %+v", d)
			}
		}, "The human chose Go ahead on your proposal P3.d1"},
		{"nobody answered", relayed, "Go ahead", state.ByTimeout, 0, "went ahead, as nobody answered", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByTimeout || !d.WithoutHuman() || st.ReviewDue != "" {
				t.Errorf("decision %+v, review %q", d, st.ReviewDue)
			}
		}, "Nobody answered your proposal P3.d1"},
		{"nobody answered, the last one", relayed, "Go ahead", state.ByTimeout, 1, "went ahead, as nobody answered", func(t *testing.T, st state.State) {
			if st.ReviewDue != "P3" {
				t.Errorf("review %q", st.ReviewDue)
			}
		}, "That was the last decision baton lets P3 make"},
		{"wait for me", relayed, "Wait for me", state.ByHuman, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByHeld || st.Run.Escalation == nil || st.Run.Escalation.Kind != "decision" {
				t.Errorf("decision %+v, escalation %+v", d, st.Run.Escalation)
			}
			if _, _, held := st.Held(); !held || len(st.Run.Notices) != 1 || !strings.Contains(st.Run.Notices[0].Text, "baton view P3") {
				t.Errorf("held %v, notices %+v", held, st.Run.Notices)
			}
		}, "The human chose Wait for me: do not do what you proposed in P3.d1"},
		{"went away", relayed, "", state.ByDeclined, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByDeclined {
				t.Errorf("decision %+v", d)
			}
		}, "Its question went away unanswered"},
		{"the lead stopped", relayed, "", state.ByLeadStopped, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByDeclined || len(st.Run.Notices) != 1 {
				t.Errorf("decision %+v, notices %+v", d, st.Run.Notices)
			}
		}, "The lead session stopped before it could put it to the human"},
		{"paused", relayed, "Pause baton", state.ByHuman, 0, "waits for the human in the worker's session", nil, "The human paused the run"},
		{"untimed, nobody answered", state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "push the tag", Because: "x", Untimed: "outward"},
			"Wait for me", state.ByTimeout, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
				if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.By != state.ByDeclined || d.WithoutHuman() {
					t.Errorf("decision %+v", d)
				}
			}, "Nobody answered it: do not do what you proposed"},
		{"own words", relayed, "use the staging clock", state.ByHuman, 0, "the human's own words", func(t *testing.T, st state.State) {
			if d := st.Decisions[0]; d.Resolved == nil || d.Resolved.Answer != "use the staging clock" {
				t.Errorf("decision %+v", d)
			}
		}, `in their own words: "use the staging clock"`},
		{"a block, continue", block, "Continue", state.ByHuman, 0, "goes on", func(t *testing.T, st state.State) {
			if st.Blocked != nil || st.Run.Escalation != nil {
				t.Errorf("still blocked: %+v %+v", st.Blocked, st.Run.Escalation)
			}
		}, "they have dealt with what blocked you (need a key)"},
		{"a block, own words", block, "use the spare key", state.ByHuman, 0, "goes on", nil, `in their own words: "use the spare key"`},
		{"a block nobody answered", block, "Wait for me", state.ByTimeout, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
			if st.Blocked == nil || len(st.Run.Notices) != 1 {
				t.Errorf("blocked %+v, notices %+v", st.Blocked, st.Run.Notices)
			}
		}, ""},
		{"a review, continue", review, "Continue", state.ByHuman, 0, "goes on", func(t *testing.T, st state.State) {
			if st.ReviewDue != "" || !st.Decisions[0].Reviewed {
				t.Errorf("review %q, %+v", st.ReviewDue, st.Decisions[0])
			}
		}, "reviewed the decisions P3 made without them and chose Continue"},
		{"a review, paused", review, "Pause baton", state.ByHuman, 0, "waits for the human in the worker's session", func(t *testing.T, st state.State) {
			if st.ReviewDue != "P3" {
				t.Errorf("review %q", st.ReviewDue)
			}
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := subRun(t, c.q, now.Add(-time.Minute))
			q := c.q
			q.Deadline = now
			state.AnswerRelay(&q, c.by, c.answer, now)
			if got := applyRelay(&st, q, c.maxAuto, now); got != c.result {
				t.Errorf("result %q, want %q", got, c.result)
			}
			if c.check != nil {
				c.check(t, st)
			}
			mail := st.Mail()
			if c.tell == "" {
				if len(mail) != 0 {
					t.Errorf("told %+v", mail)
				}
			} else if len(mail) != 1 || mail[0].Kind != state.MessageAnswer || !strings.Contains(mail[0].Text, c.tell) {
				t.Errorf("told %+v, want %q", mail, c.tell)
			}
			// Applied again (the host ended between its two writes): a question settled now changes nothing.
			if c.q.Kind == state.RelayProposal || c.result == "goes on" {
				n := len(st.Inbox)
				if got := applyRelay(&st, q, c.maxAuto, now); got != "settled in the worker's session already" || len(st.Inbox) != n {
					t.Errorf("applied twice: %q, %d messages", got, len(st.Inbox))
				}
			}
		})
	}
}

// relayCrew is a crew whose worker on P1 is at work, idle, with a proposal the lead was to put to the
// human.
func relayCrew(t *testing.T) *crew {
	t.Helper()
	x := newCrew(t, 1)
	x.tick(time.Second)
	x.started("P1")
	x.tick(time.Second)
	sub := x.sub("P1")
	sub.Update(func(st *state.State) error {
		state.Start(st, x.now, state.Origin{})
		_, err := state.AddProposal(st, state.Decision{What: "use a fake clock", Because: "drift", Undo: "revert", Relayed: true}, x.now)
		st.Run.TurnOpen, st.Run.LastActivity, st.Run.LastStop = false, x.now, x.now
		return err
	})
	if err := sub.Relay(state.Relay{Kind: state.RelayProposal, Decision: "P1.d1", What: "use a fake clock", Because: "drift"}); err != nil {
		t.Fatal(err)
	}
	return x
}

// The coordinator applies the human's answer to the worker's sub-run, and the worker's own Loop gives it
// to its idle session.
func TestTheCoordinatorAppliesAnAnswer(t *testing.T) {
	x := relayCrew(t)
	x.lead.Update(func(st *state.State) error {
		r := st.FrontRelay()
		state.IssueRelay(r, "baton: worker P1: q", x.now.Add(5*time.Minute), x.now)
		state.AnswerRelay(r, state.ByHuman, "Go ahead", x.now)
		return nil
	})
	x.tick(time.Second)
	if st := x.state(); st.Relay[0].Applied.IsZero() || !x.has("relay_applied", `"phase":"P1"`, `"result":"goes ahead"`) {
		t.Fatalf("relay %+v\n%s", st.Relay, x.events())
	}
	sst, _ := x.sub("P1").Load()
	if d := sst.Decisions[0]; d.Resolved == nil || d.Resolved.Answer != "Go ahead" {
		t.Fatalf("the worker's proposal: %+v", d)
	}
	for range 5 {
		x.tick(time.Second)
	}
	if got := strings.Join(x.typedTo["P1"].text, "\n"); !strings.Contains(got, decide.MailTyped([]string{state.MessageAnswer})) {
		t.Errorf("typed into the worker: %q", got)
	}
	x.tick(time.Second)
	if n := strings.Count(x.events(), `"kind":"relay_applied"`); n != 1 {
		t.Errorf("applied %d times", n)
	}
}

// A question nobody needs answered any more is withdrawn: settled in the worker's own session, or its
// worker gone.
func TestAnUnneededQuestionIsWithdrawn(t *testing.T) {
	for _, c := range []struct {
		name string
		do   func(x *crew)
		why  string
	}{
		{"settled in the worker's session", func(x *crew) {
			x.sub("P1").Update(func(st *state.State) error { return state.Resolve(st, "P1.d1", state.ByHuman, "no", x.now) })
		}, "it was settled in the worker's own session"},
		{"its worker gone", func(x *crew) {
			x.lead.Update(func(st *state.State) error { _, err := state.Unassign(st, x.pl, "P1", "gone", x.now); return err })
		}, "its worker is gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := relayCrew(t)
			c.do(x)
			x.tick(time.Second)
			st := x.state()
			if r := st.Relay[0]; r.Answered == nil || r.Answered.By != state.ByWithdrawn || r.Applied.IsZero() {
				t.Fatalf("relay %+v", r)
			}
			if !x.has("relay_withdrawn", `"phase":"P1"`, c.why) || !strings.Contains(x.announced(), "needs no answer any more: "+c.why) {
				t.Errorf("events:\n%s\nannounced %q", x.events(), x.announced())
			}
		})
	}
}
