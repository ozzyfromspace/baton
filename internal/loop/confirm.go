package loop

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A QA phase whose done owed one full run once the rerun it verified on passed verifies on that run from then
// on (state/confirm.go): Settle queues it (Settled.Confirms), and settle tells the human and logs it.

// confirmLines are the lines for the human of the confirmations st's Settle queued (out.Confirms).
func confirmLines(st state.State, out state.Settled) []string {
	var says []string
	for _, q := range out.Confirms {
		if ps := st.Phases[q.Phase]; ps != nil && ps.Confirm != nil {
			says = append(says, "baton: "+decide.ConfirmQueued(q.Phase, *ps.Confirm))
		}
	}
	return says
}

// confirmed logs the confirmations Settle queued (out.Confirms) in st, as settle left it, and, from a phase
// agent's loop, leaves their lines for the human in the run it works for: nobody watches its session.
func (l *Loop) confirmed(st state.State, out state.Settled) {
	for _, q := range out.Confirms {
		ps := st.Phases[q.Phase]
		if ps == nil || ps.Confirm == nil {
			continue
		}
		ev := ps.Confirm.Fields()
		ev["phase"], ev["job"], ev["by"] = q.Phase, q.ID, "settle"
		l.Store.Event("qa_confirm_queued", ev)
	}
	if l.Store.Worker() == "" || len(out.Confirms) == 0 {
		return
	}
	if up, err := l.Store.ParentRun(); err == nil {
		lines := confirmLines(st, out)
		up.Update(func(st *state.State) error { st.Run.Announce = append(st.Run.Announce, lines...); return nil })
	}
}
