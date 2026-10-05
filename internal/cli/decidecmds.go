package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The two ways the model reaches the human short of stopping the run (docs/escalation.md): a note says
// what it already did, and a proposal says what it will do unless the human objects by a deadline.

func init() {
	register("note", "tell the human about a reversible step you took without them; the run continues: note <what you did> [--undo HOW]", cmdNote)
	register("propose", "put a reversible way forward to the human; it goes ahead if nobody answers by the deadline: propose <action> --because WHY --undo HOW [--tried TEXT]", cmdPropose)
}

// refusal is a command baton turned down, decided under the state lock so that nothing changes. why is
// the short reason the refused event records.
type refusal struct{ why, text string }

func (r refusal) Error() string { return r.text }

// refused reports err to the model; a refusal is also logged.
func refused(io IO, s *state.Store, command string, err error) int {
	var r refusal
	if errors.As(err, &r) {
		s.Event("refused", map[string]any{"command": command, "why": r.why})
	}
	return fail(io, "%v", err)
}

// reviewRefusal turns down a note or proposal once a phase's decisions without the human reached the cap.
func reviewRefusal(st *state.State) error {
	return refusal{"review due", fmt.Sprintf("not recorded — %s has made as many decisions without the human as baton allows before they review them. "+
		"End your turn now: baton stops for the human to review them.", st.ReviewDue)}
}

func cmdNote(args []string, io IO) int {
	p, err := parseArgs(args, []string{"undo"}, nil)
	what := decide.Sanitize(strings.Join(p.pos, " "))
	if err != nil || what == "" {
		return fail(io, "usage: baton note \"<what you did, and why>\" [--undo \"<how to reverse it>\"]%s", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	most := config.EscalationFromEnv(io.Env).MaxAuto
	var d state.Decision
	var count int
	st, err := s.Update(func(st *state.State) error {
		if st.ReviewDue != "" {
			return reviewRefusal(st)
		}
		var err error
		if d, err = state.AddNote(st, what, decide.Sanitize(p.vals["undo"]), io.Now()); err != nil {
			return err
		}
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "note", Text: what, At: io.Now()})
		if count = st.Unattended(d.Phase); decide.CapReached(count, most) {
			st.ReviewDue = d.Phase
		}
		return nil
	})
	if err != nil {
		return refused(io, s, "note", err)
	}
	s.Event("note", map[string]any{"id": d.ID, "phase": d.Phase, "what": d.What, "undo": d.Undo})
	tally := fmt.Sprintf("%d %s without the human in %s", count, plural(count, "decision", "decisions"), d.Phase)
	if most > 0 {
		tally = fmt.Sprintf("%d of %d decisions without the human in %s", count, most, d.Phase)
	}
	if st.ReviewDue != "" {
		s.Event("review_due", map[string]any{"phase": d.Phase, "decisions": count})
		fmt.Fprintf(io.Out, "baton: noted (%s, %s). That is as many as baton lets a phase make before the human reviews them: "+
			"end your turn now, and baton stops for the human to review them.\n", d.ID, tally)
		return 0
	}
	fmt.Fprintf(io.Out, "baton: noted (%s, %s); the human will be told. Carry on with %s.\n", d.ID, tally, d.Phase)
	return 0
}

func cmdPropose(args []string, io IO) int {
	p, err := parseArgs(args, []string{"because", "undo", "tried"}, nil)
	action, because, undo := decide.Sanitize(strings.Join(p.pos, " ")), decide.Sanitize(p.vals["because"]), decide.Sanitize(p.vals["undo"])
	if err != nil || action == "" || because == "" || undo == "" {
		return fail(io, "usage: baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\" [--tried \"<what you tried>\"]%s\n"+
			"A proposal is an action that makes progress and can be undone. Waiting, doing nothing or stopping is not a proposal; "+
			"if every way forward is irreversible, run baton blocked \"<why>\" --tried \"<what you tried>\" instead.", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if !hosted(io) {
		s.Event("refused", map[string]any{"command": "propose", "why": "not hosted"})
		return fail(io, "not recorded — this session is not hosted by baton, so nothing would put your proposal to the human or go ahead with it. Ask the human directly.")
	}
	timeout := config.EscalationFromEnv(io.Env).Timeout
	d := state.Decision{What: action, Because: because, Undo: undo, Tried: decide.Sanitize(p.vals["tried"])}
	var q decide.Question
	if term, outward := decide.Outward(action); outward {
		d.Untimed = decide.UntimedReason(term)
		q = decide.UntimedQuestion(because, action, d.Untimed)
	} else {
		d.Deadline = decide.Deadline(io.Now(), timeout)
		q = decide.ProposalQuestion(because, action, d.Deadline)
	}
	d.Question = q.Text
	_, err = s.Update(func(st *state.State) error {
		switch pending := st.PendingProposal(); {
		case st.Mode != state.ModeRunning:
			return refusal{"not running", fmt.Sprintf("not recorded — baton is not running the plan (mode: %s), so nothing would go ahead with a proposal. Ask the human directly.", st.Mode)}
		case st.BoundaryOwed || st.CheckpointOwed || st.Run.Compaction.InFlight():
			return refusal{"compaction owed", "not recorded — baton must compact the context first. End your turn now; if the proposal still applies after the compaction, make it then."}
		case pending != nil:
			return refusal{"proposal pending", pendingRefusal(*pending)}
		case st.ReviewDue != "":
			return reviewRefusal(st)
		}
		var err error
		d, err = state.AddProposal(st, d, io.Now())
		return err
	})
	if err != nil {
		return refused(io, s, "propose", err)
	}
	fields := map[string]any{"id": d.ID, "phase": d.Phase, "action": d.What, "because": d.Because, "undo": d.Undo}
	if d.Tried != "" {
		fields["tried"] = d.Tried
	}
	if d.Untimed != "" {
		fields["untimed"] = d.Untimed
	} else {
		fields["deadline"] = d.Deadline.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	s.Event("proposed", fields)
	if d.Untimed != "" {
		fmt.Fprintf(io.Out, "baton: proposal %s recorded, with no deadline: %s, so only the human can let it go ahead. Now put it to them: %s. "+
			"Do nothing else until it is answered; then follow what baton tells you.\n", d.ID, d.Untimed, q.Call())
		return 0
	}
	fmt.Fprintf(io.Out, "baton: proposal %s recorded. Now put it to the human: %s. If nobody answers by %s, baton picks Go ahead for them, and you do it. "+
		"Do nothing else until it is answered; then follow what baton tells you.\n", d.ID, q.Call(), d.Deadline.Local().Format("15:04"))
	return 0
}

// pendingRefusal turns down a second proposal while one waits for its answer.
func pendingRefusal(p state.Decision) string {
	if !p.Asked {
		return fmt.Sprintf("not recorded — your proposal %s is still waiting to be put to the human. Put it to them first: %s.",
			p.ID, decide.Question{Text: p.Question, Options: decide.ProposalOptions}.Call())
	}
	return fmt.Sprintf("not recorded — your proposal %s (%s) is still waiting for the human's answer. One proposal at a time: wait for it, then follow what baton tells you.", p.ID, p.What)
}
