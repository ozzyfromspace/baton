package hooks

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Questions baton issues. baton tells the model the exact AskUserQuestion call to make and records the
// question as issued. A call counts as baton's own only when it matches exactly: one question, the same
// text (whitespace aside), the same option labels in the same order, single-select. Only such a call
// gets past PreToolUse while a plan runs, only its dialog is marked as baton's, and only its answer
// acts. A question that merely starts with "baton:" is the model's own.

// option is one answer baton offers in a question of its own.
type option struct{ label, description string }

// escalationOptions are the answers to an escalation.
var escalationOptions = []option{
	{"Continue", "I've handled it; carry on with the plan"},
	{"Pause baton", "I'll take it from here"},
}

// warnOptions are the context question's answers. The default, Keep going, is third on purpose: baton
// types 3 when nobody answers, and in a permission prompt 3 is "No" (docs/research/escalation.md).
var warnOptions = []option{
	{"Checkpoint now", "Finish the current step, save notes and compact; this phase continues from the notes"},
	{"Pause baton", "I'm taking over; baton stops driving the plan"},
	{"Keep going", "Carry on; Claude Code compacts on its own when the context is full"},
}

// issued is a question baton told the model to put to the human, word for word.
type issued struct {
	kind     string // the dialog kind: "escalation" or "context_warning"
	question string
	options  []option
}

// call spells out the AskUserQuestion call that puts q to the human.
func (q issued) call() string {
	return fmt.Sprintf("call the AskUserQuestion tool with exactly one question, %q (header \"baton\"), and exactly %d options, in this order: %s; not multi-select",
		q.question, len(q.options), optionList(q.options))
}

// optionList spells out options for the model: "A" (description: "…"), "B" (description: "…").
func optionList(opts []option) string {
	var parts []string
	for _, o := range opts {
		parts = append(parts, fmt.Sprintf("%q (description: %q)", o.label, o.description))
	}
	return strings.Join(parts, ", ")
}

// issuedQuestions are the questions baton has issued and not yet had answered: the open escalation's
// first, since it is the one the run waits on.
func issuedQuestions(st *state.State) []issued {
	var out []issued
	if e := st.Run.Escalation; e != nil && e.Question != "" {
		out = append(out, issued{"escalation", e.Question, escalationOptions})
	}
	if q := st.Run.WarnQuestion; q != "" {
		out = append(out, issued{"context_warning", q, warnOptions})
	}
	return out
}

// matchIssued returns the issued question an AskUserQuestion call puts to the human exactly as issued.
func matchIssued(st *state.State, in map[string]any) (issued, bool) {
	ti, _ := in["tool_input"].(map[string]any)
	list, _ := ti["questions"].([]any)
	if len(list) != 1 {
		return issued{}, false
	}
	q, _ := list[0].(map[string]any)
	if multi, _ := q["multiSelect"].(bool); multi {
		return issued{}, false
	}
	opts, _ := q["options"].([]any)
	for _, iq := range issuedQuestions(st) {
		if normalize(str(q, "question")) != normalize(iq.question) || len(opts) != len(iq.options) {
			continue
		}
		same := true
		for i, o := range opts {
			m, _ := o.(map[string]any)
			if normalize(str(m, "label")) != iq.options[i].label {
				same = false
			}
		}
		if same {
			return iq, true
		}
	}
	return issued{}, false
}

// lookupIssued finds the issued question an answer is for, by its text.
func lookupIssued(st *state.State, question string) (issued, bool) {
	for _, iq := range issuedQuestions(st) {
		if normalize(question) == normalize(iq.question) {
			return iq, true
		}
	}
	return issued{}, false
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
	return normalize(s)
}

// normalize collapses runs of whitespace, so a question matches whatever line breaks it went through.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
