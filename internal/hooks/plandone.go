package hooks

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// mailAtEnd refuses the lead's stop in a complete plan with baton's messages, if it has any: the one that a
// phase agent's landing completed the plan (state.MessageDone, loop's tellComplete) may land while the
// lead's turn runs, and it is given at that turn's end, as at any stop, for the model to sum the run up,
// rather than waiting for the host to wake an idle session.
func mailAtEnd(st *state.State, d *decision) {
	if len(st.Mail()) == 0 {
		return
	}
	d.mail = state.TakeMail(st)
	var texts []string
	for _, m := range d.mail {
		texts = append(texts, m.Text)
	}
	d.refuse(strings.Join(texts, "\n\n"))
}
