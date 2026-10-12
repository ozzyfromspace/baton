package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's question names its phase, and its third option is what baton picks when nobody answers:
// a timed proposal goes ahead, and anything else waits for the human, who has it again with /baton run.
func TestRelayQuestions(t *testing.T) {
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	for _, c := range []struct {
		name    string
		r       state.Relay
		text    string
		labels  string
		dflt    string
		waitFor string // the description that says how the human answers later
	}{
		{"timed proposal", state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "Use a fake \"clock\"", Because: "the clock is flaky."},
			"baton: the agent on P3: the clock is flaky. Unless you answer by 15:33, I will use a fake 'clock'.", "Wait for me|Pause baton|Go ahead", "Go ahead",
			"Don't do it yet; /baton run asks me again"},
		{"untimed proposal", state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "push the tag", Because: "the release needs it", Untimed: "'push' looks outward-facing or irreversible"},
			"baton: the agent on P3: the release needs it. I propose to push the tag, and will not do it without you ('push' looks outward-facing or irreversible).",
			"Go ahead|Pause baton|Wait for me", "Wait for me", "Leave P3 waiting; /baton run asks me again"},
		{"block", state.Relay{Phase: "P4", Kind: state.RelayBlocked, Because: "the API key expired."},
			"baton: the agent on P4 is blocked: the API key expired. Only P4 waits for you; the rest of the run goes on.", "Continue|Pause baton|Wait for me", "Wait for me",
			"Leave P4 waiting; /baton run asks me again"},
		{"review", state.Relay{Phase: "P4", Kind: state.RelayReview, Count: 3},
			"baton: the agent on P4 has made 3 decisions without you, as many as it may before you review them; /baton status lists each with its undo. Let P4 go on?",
			"Continue|Pause baton|Wait for me", "Wait for me", "Leave P4 waiting; /baton run asks me again"},
	} {
		q := RelayQuestion(c.r, deadline)
		if q.Text != c.text {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, q.Text, c.text)
		}
		var labels []string
		for _, o := range q.Options {
			labels = append(labels, o.Label)
		}
		if got := strings.Join(labels, "|"); got != c.labels {
			t.Errorf("%s: options %s, want %s", c.name, got, c.labels)
		}
		if got := RelayDefault(c.r); got != c.dflt || q.Options[2].Label != got {
			t.Errorf("%s: baton picks %q, want %q, third", c.name, got, c.dflt)
		}
		if !strings.Contains(optionList(q.Options), c.waitFor) {
			t.Errorf("%s: no option says %q: %s", c.name, c.waitFor, optionList(q.Options))
		}
		if q.Text != Sanitize(q.Text) {
			t.Errorf("%s: not sanitized: %q", c.name, q.Text)
		}
	}
}

// What a phase agent hears back names its proposal, its block or its review, and what to do next; what
// the human reads names the agent's phase.
func TestRelayAnswersForTheWorker(t *testing.T) {
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	p := state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1", What: "use a fake clock"}
	b := state.Relay{Phase: "P3", Kind: state.RelayBlocked, Because: "the key expired"}
	for _, c := range []struct {
		got  string
		want []string
	}{
		{RelayGoAhead("P3.d1", "use a fake clock", "P3"), []string{"Go ahead on your proposal P3.d1", "Do what you proposed now: use a fake clock", "carry on with P3"}},
		{RelayWentAhead("P3.d1", "use a fake clock", "drop the fake", "P3", deadline, false), []string{"Nobody answered your proposal P3.d1 by 15:33", "undone with: drop the fake", "carry on with P3"}},
		{RelayWentAhead("P3.d1", "use a fake clock", "drop the fake", "P3", deadline, true), []string{"the last decision baton lets P3 make", "review the decisions"}},
		{RelayHeld("P3.d1", "use a fake clock", "P3", "The human chose Wait for me"), []string{"The human chose Wait for me: do not do what you proposed in P3.d1 (use a fake clock)", "P3 waits for the human", "gives you their answer here"}},
		{RelayContinue(state.RelayBlocked, "the key expired", "P3"), []string{"dealt with what blocked you (the key expired)", "Carry on with P3"}},
		{RelayContinue(state.RelayReview, "", "P3"), []string{"reviewed the decisions P3 made", "Carry on with P3"}},
		{RelayOwnWords(p, "use the staging clock", "P3"), []string{`proposal P3.d1 (use a fake clock) in their own words: "use the staging clock"`, "not a Go ahead"}},
		{RelayOwnWords(b, "try the spare key", "P3"), []string{`block (the key expired) in their own words: "try the spare key"`}},
		{RelayWaits("P3", "it is blocked: the key expired"), []string{"the agent on P3 waits for you (it is blocked: the key expired)", "/baton run asks you again"}},
		{RelayWithdrawn("P3", "its agent is gone"), []string{"the agent on P3 needs no answer any more: its agent is gone"}},
		{RelayPaused("P3"), []string{"paused", "the agent on P3 waits for you", "/baton run asks you again"}},
		{RelayAgain("P3"), []string{"the agent on P3 waits for the human", "asks them its question again", "nothing of it is yours to do"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%q lacks %q", c.got, w)
			}
		}
	}
	if got := MailTyped([]string{state.MessageAnswer}); !strings.Contains(got, "Your question has its answer") {
		t.Errorf("typed %q", got)
	}
	for kind, want := range map[string]string{state.MessageAnswer: "Your question has its answer.", state.MessageLand: "Your work did not land yet", "": "baton has a message for you."} {
		if got := AgentMail([]string{kind}); !strings.Contains(got, want) || !strings.HasSuffix(got, "with your next tool call, or as you end this turn.") || strings.Contains(got, "attached") {
			t.Errorf("an agent's %q: %q", kind, got)
		}
	}
}

func TestRelayOutcome(t *testing.T) {
	p := state.Relay{Phase: "P3", Kind: state.RelayProposal, Decision: "P3.d1"}
	b := state.Relay{Phase: "P3", Kind: state.RelayBlocked}
	for _, c := range []struct {
		r      state.Relay
		answer string
		want   string
	}{
		{p, "Go ahead", OutcomeGo}, {p, "Wait for me", OutcomeWait}, {p, "", OutcomeWait}, {p, "Pause baton", OutcomePause},
		{p, "Continue", OutcomeWords}, {p, "use the staging clock", OutcomeWords},
		{b, "Continue", OutcomeOn}, {b, "Wait for me", OutcomeWait}, {b, "Pause baton", OutcomePause}, {b, "Go ahead", OutcomeWords},
		{b, "try the spare key", OutcomeWords},
	} {
		if got := RelayOutcome(c.r, c.answer); got != c.want {
			t.Errorf("%s answered %q: %s, want %s", c.r.Kind, c.answer, got, c.want)
		}
	}
}
