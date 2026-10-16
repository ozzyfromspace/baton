package hooks

import (
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Claude Code 2.1.302 gives a background agent in auto mode a tool to hand its final report back to the
// session that started it (SubagentHandback), tells it the call "ends your run", and, when it ends a turn
// without one, sends it back to make it (up to three times). For a phase agent a hand-back ends a turn:
// baton's next message resumes it. But it ends that turn whatever the SubagentStop hook says, so a stop
// baton would refuse (its phase not reported, a result due) is not refused at all, and a result the stop
// takes is never read: in a real gate, an agent that handed back while it waited on its job read the result
// as coming after its run, and never reported its phase. So a phase agent's hand-back is decided where its
// stop would be refused, at the call: refused with what that stop would say, and let through when the stop
// would be allowed (it waits on something that will wake it, or it is done).

// HandbackTool is Claude Code's tool through which an agent hands its report back.
const HandbackTool = "SubagentHandback"

// handbackAnswer is how baton answers a phase agent's hand-back it refuses: why (its stop's refusal), or what it
// had for the agent first (results, mail).
type handbackAnswer struct {
	why     string
	deliver delivery
	mail    []state.Message
}

// handedBack refuses a phase agent's hand-back that its stop would refuse (heldHandback), and reports
// whether it did. A call that is not a phase agent's hand-back, or a run it cannot read, goes on.
func (h *handlers) handedBack(c Context) (Result, bool) {
	if str(c.Input, "tool_name") != HandbackTool || c.Agent != "" { // a subagent of a loop's own model is none of baton's
		return Result{}, false
	}
	s, err := h.d.Open(c)
	if err != nil || s == nil || s.Worker() == "" {
		return Result{}, false
	}
	pl, _ := s.LoadPlan()
	var hb handbackAnswer
	var held bool
	var phase string
	if _, _, err := h.update(c, func(st *state.State, s *state.Store) error {
		phase = st.Current
		hb, held = heldHandback(st, c.Now, words(c, s), phaseTitle(pl, st.Current))
		return nil
	}); err != nil || !held {
		return Result{}, false
	}
	fields := map[string]any{"phase": phase}
	var why string
	say := decide.HandbackKept(phase)
	switch {
	case hb.deliver.any():
		fields["why"] = "results"
		why = decide.HandbackFirst + "\n\n" + hb.deliver.text(s, config.AggregatesFromEnv(c.Env))
		say = hb.deliver.says()
		hb.deliver.event(s, "handback")
	case len(hb.mail) > 0:
		fields["why"] = "mail"
		var texts []string
		for _, m := range hb.mail {
			texts = append(texts, m.Text)
			s.Event("message_delivered", map[string]any{"message": m.Kind, "via": "handback"})
		}
		why = decide.HandbackFirst + "\n\n" + strings.Join(texts, "\n\n")
	default:
		fields["why"] = "no status"
		why = hb.why
	}
	s.Event("handback_refused", fields)
	res := permission("deny", why)
	res.Output["systemMessage"] = say
	return res, true
}

// heldHandback decides a phase agent's hand-back in its sub-run's state st, as decideStop would decide the
// stop it is: refused to give the agent the results and messages due, and, while its phase is in hand and
// not reported with nothing it waits on, refused with the stop's own words (Words.NoStatus) and counted with
// the stop's refusals, so that past MaxStopBlocks it goes through as the stop would (the agent's loop
// watchdog nudges it). Everything else lets it through: the plan is not running, a compaction or the
// human's answer is owed, or it waits on a job, its proposal, its block or its own background work, each of
// which wakes it. title is how the phase is named.
func heldHandback(st *state.State, now time.Time, w decide.Words, title string) (handbackAnswer, bool) {
	var hb handbackAnswer
	if st.Mode == state.ModeIdle || st.Mode == state.ModePaused {
		return hb, false
	}
	switch {
	case len(st.Due()) > 0:
		st.Run.StopBlocks = 0
		hb.deliver = take(st, now)
		return hb, true
	case st.Mode != state.ModeRunning:
		return hb, false // its phase is done: the hand-back ends its run
	case len(st.Mail()) > 0:
		st.Run.StopBlocks = 0
		hb.mail = state.TakeMail(st)
		return hb, true
	case st.Current == "", st.Compacting(), st.ReviewDue != "", st.Run.Escalation != nil, st.Blocked != nil,
		st.OwnProposal() != nil, st.PendingProposal() != nil, len(st.CurrentJobs()) > 0, len(st.Run.BusyBackground()) > 0:
		return hb, false
	}
	if st.Run.StopBlocks++; st.Run.StopBlocks > MaxStopBlocks {
		return hb, false
	}
	hb.why = decide.HandbackHeld(w.NoStatus(st.Current, title))
	return hb, true
}
