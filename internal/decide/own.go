package decide

import (
	"fmt"
	"strings"
	"time"
)

// The lead's model may put a question of its own to the human in a turn they started (AskUserQuestion).
// While a plan runs, nothing waits on the human for it longer than escalation_timeout: where baton's mod
// runs, it answers each question of the call then for the human, and the model is told baton chose.

// Recommended is how the model marks the option it recommends: Claude Code asks it to end that option's
// label with it.
const Recommended = "(Recommended)"

// OwnAnswer is the option baton chooses for the human, at its deadline, for a question the lead's model
// put to them (text, and its options' labels in order): the one the model marked Recommended, else its
// first, where Claude Code asks the model to put its recommendation. A question in the shape of baton's
// own (its text starts "baton:", with three options or more) that baton no longer has open gets its third,
// where every question of baton's keeps its default (EscalationOptions). "" when it offers none.
func OwnAnswer(text string, options []string) string {
	for _, o := range options {
		if strings.Contains(strings.ToLower(o), strings.ToLower(Recommended)) {
			return o
		}
	}
	switch {
	case len(options) == 0:
		return ""
	case len(options) >= 3 && strings.HasPrefix(strings.TrimSpace(text), "baton:"):
		return options[2]
	}
	return options[0]
}

// QA is a question and the answer baton gave it.
type QA struct{ Question, Answer string }

// AnsweredOwn is what baton says to the human (say) and tells the model (tell) when nobody answered the
// lead model's own question call by at, so baton gave each of its questions the answer in qas.
func AnsweredOwn(qas []QA, at time.Time) (say, tell string) {
	var short, full []string
	for _, qa := range qas {
		short = append(short, fmt.Sprintf("%q → %q", cutRunes(Normalize(qa.Question), 80), qa.Answer))
		full = append(full, fmt.Sprintf("%q → %q", Normalize(qa.Question), qa.Answer))
	}
	when := at.Local().Format("15:04")
	say = fmt.Sprintf("baton: nobody answered Claude's question by %s, so baton chose for you — %s", when, strings.Join(short, "; "))
	tell = fmt.Sprintf("[baton] Nobody answered your question by %s, so baton chose for the human: %s. "+
		"It is baton's choice, not theirs. Go on with it, keep what you do on it reversible where you can, "+
		"and tell the human in your summary that baton chose it.", when, strings.Join(full, "; "))
	return say, tell
}

// cutRunes cuts s to n runes, marking the cut.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
