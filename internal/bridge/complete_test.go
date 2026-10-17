package bridge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A complete names its model and its prompt; one naming neither, or with a negative cap or time limit, is
// refused. The command is written with all it says, and its event names the model and the prompt's size.
func TestACompleteNamesItsModelAndPrompt(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	for _, bad := range []Command{
		{Kind: Complete, Instance: "inst", Prompt: "Why?"},
		{Kind: Complete, Instance: "inst", Model: "haiku"},
		{Kind: Complete, Instance: "inst", Model: "haiku", Prompt: "Why?", MaxTokens: -1},
		{Kind: Complete, Instance: "inst", Model: "haiku", Prompt: "Why?", TimeoutMs: -1},
	} {
		if _, err := r.b.Issue(bad); err == nil {
			t.Errorf("issued %+v", bad)
		}
	}
	c := r.issue(Command{Kind: Complete, Model: "haiku", System: "One line.", Prompt: "Why did it fail?", MaxTokens: 400, TimeoutMs: 60000})
	var got Command
	if data, err := os.ReadFile(r.b.CommandPath(c.ID)); err != nil || json.Unmarshal(data, &got) != nil {
		t.Fatalf("the complete as written: %s (%v)", data, err)
	}
	if got.Kind != Complete || got.Model != "haiku" || got.System != "One line." || got.Prompt != "Why did it fail?" || got.MaxTokens != 400 || got.TimeoutMs != 60000 {
		t.Errorf("written %+v", got)
	}
	if ev := strings.Join(r.events, "\n"); !strings.Contains(ev, `mod_command {"chars":25,"command":"complete","id":1,"model":"haiku"}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// The ack of a complete goes to the host that issued it, not the inbox: it takes it once (Answer), with the
// answer, how long the call took and what it used. Another host instance finds nothing.
func TestACompletesAnswerIsKeptForItsHost(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	c := r.issue(Command{Kind: Complete, Model: "haiku", Prompt: "Why?"})
	if _, ok, err := r.b.Answer("inst", c.ID); ok || err != nil {
		t.Fatalf("an answer before the ack: %v %v", ok, err)
	}
	usage := &Usage{Input: 6959, Output: 120, CacheRead: 1, CacheWrite: 2}
	a, err := r.b.Accept(Report{Seq: 1, Kind: Ack, Instance: "inst", Command: c.ID, OK: true, Result: "The environment's.", Ms: 3511, Usage: usage})
	if err != nil || a.Acked == nil || a.Acked.Kind != Complete {
		t.Fatalf("the ack: %+v %v", a, err)
	}
	if rec := r.load(); len(rec.Inbox) != 0 || len(rec.Answers) != 1 || len(rec.Open) != 0 || len(r.files()) != 0 {
		t.Errorf("inbox %+v, answers %+v, open %+v, files %q", rec.Inbox, rec.Answers, rec.Open, r.files())
	}
	if _, ok, _ := r.b.Answer("inst-2", c.ID); ok {
		t.Error("another host instance took the answer")
	}
	rep, ok, err := r.b.Answer("inst", c.ID)
	if !ok || err != nil || rep.Result != "The environment's." || rep.Ms != 3511 || *rep.Usage != *usage {
		t.Errorf("the answer: %+v %v %v", rep, ok, err)
	}
	if _, ok, _ := r.b.Answer("inst", c.ID); ok {
		t.Error("the answer was taken twice")
	}
	if ev := strings.Join(r.events, "\n"); !strings.Contains(ev, `mod_ack {"command":1,"command_kind":"complete","input_tokens":6962,"ms":3511,"ok":true,"output_tokens":120,"result":"The environment's.","seq":1}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// The mod's ack of a complete, as it writes it (plugin/hooks/baton.js), reads whole: what the call used
// is in the API's spelling.
func TestTheModsAckOfACompleteReads(t *testing.T) {
	const ack = `{"kind":"ack","command":7,"ok":true,"result":"Advice.","usage":{"input_tokens":6959,"output_tokens":120,"cache_read_input_tokens":3,"cache_creation_input_tokens":4},"ms":3511,"seq":4,"session":"sess"}`
	var rep Report
	if err := json.Unmarshal([]byte(ack), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Command != 7 || !rep.OK || rep.Result != "Advice." || rep.Ms != 3511 || rep.Usage == nil || *rep.Usage != (Usage{Input: 6959, Output: 120, CacheRead: 3, CacheWrite: 4}) {
		t.Errorf("read %+v", rep)
	}
}

// Why a complete has no answer comes in its ack: the model's error, or the mod reloaded while it ran.
func TestACompleteWithNoAnswerSaysWhy(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	c1 := r.issue(Command{Kind: Complete, Model: "haiku", Prompt: "Why?"})
	c2 := r.issue(Command{Kind: Complete, Model: "haiku", Prompt: "Why?"})
	r.record(Report{Seq: 1, Kind: Ack, Command: c1.ID, Reason: "api-error", Error: "api-error 529 overloaded"})
	r.record(Report{Seq: 2, Kind: Ack, Command: c2.ID, Interrupted: true})
	if rep, ok, _ := r.b.Answer("inst", c1.ID); !ok || rep.OK || rep.Reason != "api-error" || rep.Error != "api-error 529 overloaded" {
		t.Errorf("c1: %+v %v", rep, ok)
	}
	if rep, ok, _ := r.b.Answer("inst", c2.ID); !ok || !rep.Interrupted {
		t.Errorf("c2: %+v %v", rep, ok)
	}
}

// The ack of a complete taken back (Void: its host gave up on it) is kept for no one; the record keeps at
// most MaxAnswers, and a host claiming the bridge drops another instance's.
func TestAnswersNoOneWaitsForGo(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	c := r.issue(Command{Kind: Complete, Model: "haiku", Prompt: "Why?"})
	if ok, _ := r.b.Void("inst", c.ID); !ok {
		t.Fatal("not voided")
	}
	r.record(Report{Seq: 1, Kind: Ack, Command: c.ID, OK: true, Result: "Late."})
	if rec := r.load(); len(rec.Answers) != 0 || len(rec.Inbox) != 1 {
		t.Errorf("a late answer: answers %+v, inbox %+v", rec.Answers, rec.Inbox)
	}
	seq := 1
	for range MaxAnswers + 3 {
		seq++
		c := r.issue(Command{Kind: Complete, Model: "haiku", Prompt: "Why?"})
		r.record(Report{Seq: seq, Kind: Ack, Command: c.ID, OK: true, Result: "So."})
	}
	rec := r.load()
	if len(rec.Answers) != MaxAnswers || rec.Answers[0].Command != 5 {
		t.Errorf("kept %d answers, the oldest for %d", len(rec.Answers), rec.Answers[0].Command)
	}
	if err := r.b.Claim("inst-new"); err != nil {
		t.Fatal(err)
	}
	if rec := r.load(); len(rec.Answers) != 0 {
		t.Errorf("another host's answers kept: %d", len(rec.Answers))
	}
}
