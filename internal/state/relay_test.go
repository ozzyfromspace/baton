package state

import (
	"testing"
	"time"
)

// A phase agent's question joins the relay queue once, however often its commands and hooks queue it, and
// even once it is answered; a new question of the same phase queues after it.
func TestQueueRelay(t *testing.T) {
	st := New()
	answered := &Resolution{By: ByHuman, At: t0}
	for _, c := range []struct {
		name   string
		r      Relay
		answer bool // answer the queue's last item afterwards
		queued bool
	}{
		{"a proposal", Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d1"}, false, true},
		{"the same proposal again", Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d1"}, false, false},
		{"another worker's block", Relay{Phase: "P4", Kind: RelayBlocked, Since: t0}, false, true},
		{"the same block again", Relay{Phase: "P4", Kind: RelayBlocked, Since: t0}, true, false},
		{"the same block, answered", Relay{Phase: "P4", Kind: RelayBlocked, Since: t0}, false, false},
		{"a new block", Relay{Phase: "P4", Kind: RelayBlocked, Since: t0.Add(time.Minute)}, false, true},
		{"a review of the proposing phase", Relay{Phase: "P3", Kind: RelayReview, Since: t0}, false, true},
		{"the next proposal", Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d2", Issued: t0, Question: "q", Asked: true}, false, true},
	} {
		n := len(st.Relay)
		if got := QueueRelay(&st, c.r, t0.Add(time.Minute)); got != c.queued || len(st.Relay) != n+btoi(c.queued) {
			t.Fatalf("%s: queued %v, %d in the queue; want %v", c.name, got, len(st.Relay), c.queued)
		}
		if c.answer {
			st.Relay[len(st.Relay)-1].Answered = answered
		}
	}
	if last := st.Relay[len(st.Relay)-1]; !last.Queued.Equal(t0.Add(time.Minute)) || !last.Issued.IsZero() || last.Question != "" || last.Asked {
		t.Errorf("queued %+v: it waits to be issued", last)
	}
}

// The front question is issued to the mod only while the plan runs and nothing of the lead's own waits on
// the human (a compaction of the lead does not hold it: the mod asks it, not the lead's model); once
// issued, it stays the one asked until it is answered, and the next one waits behind it.
func TestRelayDue(t *testing.T) {
	running := func() State {
		st := New()
		st.Mode, st.Current = ModeRunning, "P1"
		QueueRelay(&st, Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d1"}, t0)
		QueueRelay(&st, Relay{Phase: "P4", Kind: RelayBlocked}, t0)
		return st
	}
	for _, c := range []struct {
		name string
		set  func(st *State)
		due  bool
	}{
		{"running", func(*State) {}, true},
		{"paused", func(st *State) { st.Mode = ModePaused }, false},
		{"a boundary owed", func(st *State) { st.BoundaryOwed = true }, true},
		{"a compaction queued", func(st *State) { st.Run.Compaction.Queue("checkpoint", t0) }, true},
		{"the lead escalated", func(st *State) { st.Run.Escalation = &Escalation{Kind: "stalled", Since: t0} }, false},
		{"the lead is blocked", func(st *State) { st.Blocked = &Block{Reason: "x", Since: t0} }, false},
		{"the lead's own proposal", func(st *State) { AddProposal(st, Decision{What: "x"}, t0) }, false},
		{"the lead's review", func(st *State) { st.ReviewDue = "P1" }, false},
		{"the context question", func(st *State) { st.Run.WarnQuestion = "baton: context" }, false},
		{"a plan approved mid-run", func(st *State) { st.Replan = &Replan{File: "p.md"} }, false},
		{"a worker's proposal of the run's own", func(st *State) { AddProposal(st, Decision{What: "x", Relayed: true}, t0) }, true},
	} {
		st := running()
		c.set(&st)
		if got := st.RelayDue() != nil; got != c.due {
			t.Errorf("%s: due %v, want %v", c.name, got, c.due)
		}
	}

	st := running()
	r := st.RelayDue()
	if r == nil || r.Phase != "P3" || st.RelayIssued() != nil {
		t.Fatalf("front %+v; issued %v", r, st.RelayIssued())
	}
	IssueRelay(r, "baton: the agent on P3: q", 7, "inst", t0.Add(5*time.Minute), t0)
	if st.RelayDue() != nil || st.RelayIssued() != r || r.Ask != 7 || r.Instance != "inst" {
		t.Fatalf("issued %+v: it is the one asked, and nothing else is due", *r)
	}
	st.Run.Escalation = &Escalation{Kind: "stalled", Since: t0}
	if st.RelayIssued() != r {
		t.Fatal("a question of the lead's own came after it was issued: it still waits for its answer")
	}
	st.Run.Escalation = nil
	AnswerRelay(r, ByHuman, "Go ahead", t0.Add(time.Minute))
	if next := st.RelayDue(); next == nil || next.Phase != "P4" {
		t.Fatalf("after the answer, next %+v", next)
	}
	if got := st.Unapplied(); len(got) != 1 || !got[0].Same(*r) {
		t.Fatalf("unapplied %+v", got)
	}
	if q := st.RelayOf(*r); q != r {
		t.Fatal("RelayOf")
	}
	st.RelayOf(*r).Applied = t0.Add(2 * time.Minute)
	if len(st.Unapplied()) != 0 {
		t.Fatal("applied, yet listed")
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A phase agent's question waits in its sub-run while its proposal is unresolved, or while its block or its
// review is still the one it asked about.
func TestWaits(t *testing.T) {
	st := AttachWorker(New(), "P3")
	Start(&st, t0, Origin{})
	p := Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d1"}
	b := Relay{Phase: "P3", Kind: RelayBlocked, Since: t0}
	v := Relay{Phase: "P3", Kind: RelayReview, Since: t0}
	if st.Waits(p) || st.Waits(b) || st.Waits(v) {
		t.Fatal("nothing asked, yet waiting")
	}
	AddProposal(&st, Decision{What: "x", Relayed: true}, t0)
	SetBlocked(&st, "need a key", t0)
	DueReview(&st, "P3", t0)
	if !st.Waits(p) || !st.Waits(b) || !st.Waits(v) {
		t.Fatal("asked, yet not waiting")
	}
	if st.Waits(Relay{Phase: "P3", Kind: RelayBlocked, Since: t0.Add(-time.Hour)}) {
		t.Error("an earlier block waits")
	}
	Resolve(&st, "P3.d1", ByHuman, "Go ahead", t0)
	Resume(&st)
	if st.Waits(p) || st.Waits(b) || st.Waits(v) {
		t.Error("answered, yet waiting")
	}
}

// A question answered and applied whose phase still waits on the human is asked again at the end of the
// queue, afresh; one not answered yet, or not applied yet, stays as it is.
func TestAskAgain(t *testing.T) {
	st := New()
	p := Relay{Phase: "P3", Kind: RelayProposal, Decision: "P3.d1"}
	b := Relay{Phase: "P4", Kind: RelayBlocked, Since: t0}
	QueueRelay(&st, p, t0)
	QueueRelay(&st, b, t0)
	if AskAgain(&st, p, t0) {
		t.Fatal("asked again before it was answered")
	}
	q := st.RelayOf(p)
	IssueRelay(q, "baton: the agent on P3: q", 3, "inst", t0.Add(5*time.Minute), t0)
	q.Asked, q.AskedAt = true, t0
	AnswerRelay(q, ByHuman, "Wait for me", t0.Add(time.Minute))
	if AskAgain(&st, p, t0) {
		t.Fatal("asked again before its answer was applied")
	}
	q.Applied = t0.Add(time.Minute)
	if !AskAgain(&st, p, t0.Add(time.Hour)) {
		t.Fatal("not asked again")
	}
	if len(st.Relay) != 2 || !st.Relay[0].Same(b) || !st.Relay[1].Same(p) {
		t.Fatalf("queue %+v: the question asked again goes last", st.Relay)
	}
	if r := st.Relay[1]; r.Answered != nil || !r.Applied.IsZero() || !r.Issued.IsZero() || r.Question != "" || r.Asked || r.Ask != 0 || r.Instance != "" || !r.Queued.Equal(t0.Add(time.Hour)) {
		t.Errorf("asked again %+v: it is asked afresh", r)
	}
	if AskAgain(&st, Relay{Phase: "P9", Kind: RelayBlocked}, t0) {
		t.Error("a question never queued asked again")
	}
}
