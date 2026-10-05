// Package decide is baton's policy for a run that must not stop for something it can work around
// (docs/escalation.md): which way of reaching the human fits, the questions baton puts to them, and the
// words the model reads about all of it. It lives in one place so that the CLI, the hooks and the brief
// say the same thing, and so that no text meant for a project without git mentions committing.
package decide

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Option is one answer a question of baton's offers.
type Option struct{ Label, Description string }

// Question is a question baton tells the model to put to the human, word for word. It counts as baton's
// own only in exactly that shape: one question, the same text (whitespace aside), the same labels in the
// same order, single-select.
type Question struct {
	Text    string
	Options []Option
}

// Call spells out the AskUserQuestion call that puts q to the human.
func (q Question) Call() string {
	return fmt.Sprintf("call the AskUserQuestion tool with exactly one question, %q (header \"baton\"), and exactly %d options, in this order: %s; not multi-select",
		q.Text, len(q.Options), optionList(q.Options))
}

// optionList spells out options for the model: "A" (description: "…"), "B" (description: "…").
func optionList(opts []Option) string {
	var parts []string
	for _, o := range opts {
		parts = append(parts, fmt.Sprintf("%q (description: %q)", o.Label, o.Description))
	}
	return strings.Join(parts, ", ")
}

// EscalationOptions are the answers to an escalation.
var EscalationOptions = []Option{
	{"Continue", "I've handled it; carry on with the plan"},
	{"Pause baton", "I'll take it from here"},
}

// ReviewOptions are the answers to a review of the decisions a phase made without the human.
var ReviewOptions = []Option{
	{"Continue", "I've seen them; let the run go on"},
	{"Pause baton", "I'll take it from here"},
}

// WarnOptions are the context question's answers. The default, Keep going, is third on purpose: baton
// types 3 when nobody answers, and in a permission prompt 3 is "No" (docs/research/escalation.md).
var WarnOptions = []Option{
	{"Checkpoint now", "Finish the current step, save notes and compact; this phase continues from the notes"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{"Keep going", "Carry on; Claude Code compacts on its own when the context is full"},
}

// ProposalOptions are the answers to a proposal. Go ahead is third for the same reason as Keep going.
var ProposalOptions = []Option{
	{"Wait for me", "Don't do it yet; I'll answer in the session"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{"Go ahead", "Do what you proposed"},
}

// AskFirst says to put a proposal to the human before anything else: until it is, nothing moves.
func AskFirst(id, question string) string {
	return fmt.Sprintf("put your proposal %s to the human first: %s", id, Question{question, ProposalOptions}.Call())
}

// ReviewQuestion is the question that stops the run for the human to review the decisions phase made
// without them.
func ReviewQuestion(phase string, n int) Question {
	return Question{
		Text:    fmt.Sprintf("baton: %s has made %d decisions without you, as many as it may before you review them; /baton status lists each with its undo. Let the run go on?", phase, n),
		Options: ReviewOptions,
	}
}

// Deadline is when a proposal made at now goes ahead: timeout later, rounded up to the minute, since the
// question names it as HH:MM and baton must never go ahead before the time it gave.
func Deadline(now time.Time, timeout time.Duration) time.Time {
	t := now.Add(timeout)
	if r := t.Truncate(time.Minute); r.Before(t) {
		return r.Add(time.Minute)
	}
	return t
}

// ProposalQuestion is the question that puts a timed proposal to the human. because and action must be
// sanitized already.
func ProposalQuestion(because, action string, deadline time.Time) Question {
	return Question{
		Text:    fmt.Sprintf("baton: %s. Unless you answer by %s, I will %s.", clause(because), deadline.Local().Format("15:04"), lowerFirst(clause(action))),
		Options: ProposalOptions,
	}
}

// UntimedQuestion is the question for a proposal baton will not let go ahead without the human, and why.
func UntimedQuestion(because, action, why string) Question {
	return Question{
		Text:    fmt.Sprintf("baton: %s. I propose to %s, and will not do it without you (%s).", clause(because), lowerFirst(clause(action)), why),
		Options: ProposalOptions,
	}
}

// clause trims what would double up with the punctuation around it.
func clause(s string) string { return strings.TrimRight(strings.TrimSpace(s), " .") }

// lowerFirst lowers a capital that only starts a sentence ("Commit P4" reads "commit P4"), and leaves
// one that is part of a word ("PR", "GPG") alone.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	next, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(r) && !unicode.IsUpper(next) {
		return string(unicode.ToLower(r)) + s[n:]
	}
	return s
}

// Sanitize makes text from the model safe to put inside a question baton issues. Whitespace collapses to
// single spaces, double quotes become single quotes and backslashes slashes, and other control
// characters are dropped. Quoted for the model (%q), the text then reads exactly as baton stores and
// matches it, with nothing escaped for the model to reproduce or not.
func Sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r == '\\':
			return '/'
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)
	return Normalize(s)
}

// Normalize collapses runs of whitespace, so a question matches whatever line breaks it went through.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
