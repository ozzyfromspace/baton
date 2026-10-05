package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Questions baton issues. baton tells the model the exact AskUserQuestion call to make and records the
// question as issued. A call counts as baton's own only when it matches exactly: one question, the same
// text (whitespace aside), the same option labels in the same order, single-select. Only such a call
// gets past PreToolUse while a plan runs, only its dialog is marked as baton's, and only its answer
// acts. A question that merely starts with "baton:" is the model's own.

// issued is a question baton told the model to put to the human, word for word, and what kind of
// dialog it makes.
type issued struct {
	kind string // "escalation", "context_warning" or "proposal"
	decide.Question
}

// issuedQuestions are the questions baton has issued and not yet had answered: the open escalation's
// first, since it is the one the run waits on.
func issuedQuestions(st *state.State) []issued {
	var out []issued
	if e := st.Run.Escalation; e != nil && e.Question != "" {
		out = append(out, issued{"escalation", decide.Question{Text: e.Question, Options: decide.EscalationOptions}})
	}
	if p := st.PendingProposal(); p != nil && p.Question != "" {
		out = append(out, issued{"proposal", decide.Question{Text: p.Question, Options: decide.ProposalOptions}})
	}
	if q := st.Run.WarnQuestion; q != "" {
		out = append(out, issued{"context_warning", decide.Question{Text: q, Options: decide.WarnOptions}})
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
		if decide.Normalize(str(q, "question")) != decide.Normalize(iq.Text) || len(opts) != len(iq.Options) {
			continue
		}
		same := true
		for i, o := range opts {
			m, _ := o.(map[string]any)
			if decide.Normalize(str(m, "label")) != iq.Options[i].Label {
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
		if decide.Normalize(question) == decide.Normalize(iq.Text) {
			return iq, true
		}
	}
	return issued{}, false
}
