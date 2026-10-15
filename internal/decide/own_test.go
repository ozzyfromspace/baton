package decide

import (
	"strings"
	"testing"
	"time"
)

// What baton answers for the human to a question of the lead model's own: the option it marked
// recommended, else its first; a stale question of baton's shape its default, third; none with nothing to
// choose from.
func TestOwnAnswer(t *testing.T) {
	for _, c := range []struct {
		name, text string
		options    []string
		want       string
	}{
		{"recommended", "Which database?", []string{"sqlite", "Postgres (Recommended)"}, "Postgres (Recommended)"},
		{"recommended, any case", "Which database?", []string{"sqlite", "postgres (recommended)", "mysql"}, "postgres (recommended)"},
		{"first", "Which colour?", []string{"red", "blue", "green"}, "red"},
		{"one option", "Go on?", []string{"Yes"}, "Yes"},
		{"none", "Anything else?", nil, ""},
		{"baton's shape", "baton: the model stopped 4 times", []string{"Pause baton", "Wait for me", "Continue"}, "Continue"},
		{"baton's shape, recommended", "baton: which?", []string{"A", "B (Recommended)", "C"}, "B (Recommended)"},
		{"baton: with two options", "baton: lunch?", []string{"Yes", "No"}, "Yes"},
	} {
		if got := OwnAnswer(c.text, c.options); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The human and the model are told what baton chose for each question, and the model that it is baton's
// choice, not the human's.
func TestAnsweredOwn(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.Local)
	say, tell := AnsweredOwn([]QA{{"Which database?", "Postgres (Recommended)"}, {"Which  colour?\n", "red"}}, at)
	for _, want := range []string{`"Which database?" → "Postgres (Recommended)"`, `"Which colour?" → "red"`, "12:00"} {
		if !strings.Contains(say, want) || !strings.Contains(tell, want) {
			t.Errorf("%q missing:\nsay  %s\ntell %s", want, say, tell)
		}
	}
	if !strings.HasPrefix(say, "baton: nobody answered Claude's question") || !strings.Contains(tell, "baton's choice, not theirs") {
		t.Errorf("say %s\ntell %s", say, tell)
	}
	long := strings.Repeat("x", 200)
	if say, tell := AnsweredOwn([]QA{{long, "a"}}, at); strings.Contains(say, long) || !strings.Contains(tell, long) {
		t.Errorf("a long question: say %s", say)
	}
}
