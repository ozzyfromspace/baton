package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The two ways the model reaches the human short of stopping the run (docs/design.md): a note says
// what it already did, and a proposal says what it will do unless the human objects by a deadline.

func init() {
	register("note", "tell the human about a reversible step you took without them; the run continues: note <what you did> [--undo HOW]", cmdNote)
	register("propose", "put a reversible way forward to the human; it goes ahead if nobody answers by the deadline (an outward one is answered Not now): propose <action> --because WHY --undo HOW [--wait 5m-20m] [--tried TEXT]", cmdPropose)
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
		// A note with no phase in hand is the run's: no phase's review counts it.
		if count = st.Unattended(d.Phase); d.Phase != "" && decide.CapReached(count, most) {
			state.DueReview(st, d.Phase, io.Now())
		}
		return nil
	})
	if err != nil {
		return refused(io, s, "note", err)
	}
	s.Event("note", map[string]any{"id": d.ID, "phase": d.Phase, "what": d.What, "undo": d.Undo})
	if up, err := s.ParentRun(); err == nil {
		// Nobody watches a worker's session: the human reads its note in the lead's.
		up.Update(func(st *state.State) error {
			st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: the worker on %s noted — %s (%s)", s.Worker(), d.What, d.ID))
			return nil
		})
	}
	if d.Phase == "" {
		fmt.Fprintln(io.Out, decide.NotedRun(d.ID))
		return 0
	}
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
	p, err := parseArgs(args, []string{"because", "undo", "tried", "wait"}, nil)
	action, because, undo := decide.Sanitize(strings.Join(p.pos, " ")), decide.Sanitize(p.vals["because"]), decide.Sanitize(p.vals["undo"])
	if err != nil || action == "" || because == "" || undo == "" {
		return fail(io, "usage: baton propose \"<what you will do to keep the plan moving>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\" [--tried \"<what you tried>\"] [--wait 5m]%s\n"+
			"A proposal is an action that makes progress and can be undone. Waiting, doing nothing or stopping is not a proposal; "+
			"if every way forward is irreversible, run baton blocked \"<why>\" --tried \"<what you tried>\" instead. "+decide.WaitAdvice(), errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	isHosted := hosted(io) // read before the run's lock: it reads processes
	if !isHosted {
		s.Event("refused", map[string]any{"command": "propose", "why": "not hosted"})
		return fail(io, "not recorded — this session is not hosted by baton, so nothing would put your proposal to the human or go ahead with it. Ask the human directly.")
	}
	timeout := config.EscalationFromEnv(io.Env).Timeout
	wait, waitWhy := decide.ProposalWait(p.vals["wait"], timeout)
	// A worker's proposal is put to the human by the lead session, which words the question and starts its
	// deadline when it asks it (Relayed).
	// An outward one is answered Not now at its deadline, not Go ahead (decide.Outward).
	relayed := s.Worker() != ""
	d := state.Decision{What: action, Because: because, Undo: undo, Tried: decide.Sanitize(p.vals["tried"]), Relayed: relayed, Wait: wait}
	if term, outward := decide.Outward(action); outward {
		d.Outward = decide.OutwardReason(term)
	}
	var q decide.Question
	if !relayed { // a worker's deadline counts from when it joins the relay queue (state.Relay.Due)
		d.Deadline = decide.Deadline(io.Now(), wait)
		q = decide.ProposalQuestion(because, action, d.Deadline)
		if d.Outward != "" {
			q = decide.OutwardQuestion(because, action, d.Outward, d.Deadline)
		}
		d.Question = q.Text
	}
	_, err = s.Update(func(st *state.State) error {
		if st.ReviewDue != "" {
			return reviewRefusal(st)
		}
		if err := guard(isHosted, st, "not recorded", false); err != nil {
			return err
		}
		switch pending := st.PendingProposal(); {
		case st.Mode != state.ModeRunning:
			return refusal{"not running", fmt.Sprintf("not recorded — baton is not running the plan (mode: %s), so nothing would go ahead with a proposal. Ask the human directly.", st.Mode)}
		case st.BoundaryOwed || st.CheckpointOwed || st.Run.Compaction.InFlight():
			return refusal{"compaction owed", "not recorded — baton must compact the context first. End your turn now; if the proposal still applies after the compaction, make it then."}
		case st.CheckpointDue:
			return refusal{"checkpoint due", "not recorded — " + decide.CheckpointOnResume(st.Current) + " If the proposal still applies after the compaction, make it then."}
		case pending != nil:
			return refusal{"proposal pending", pendingRefusal(*pending)}
		case !st.InHand():
			return refusal{"no phase in hand", decide.NoPhase("not recorded")}
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
	if d.Outward != "" {
		fields["outward"] = d.Outward
	}
	if !d.Deadline.IsZero() {
		fields["deadline"] = d.Deadline.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	fields["wait"], fields["chosen"] = d.Wait.String(), p.vals["wait"] != "" && waitWhy == ""
	if waitWhy != "" {
		fields["wait_refused"] = waitWhy
	}
	if relayed {
		fields["relayed"] = true
	}
	s.Event("proposed", fields)
	if relayed {
		if err := s.Relay(state.Relay{Kind: state.RelayProposal, Decision: d.ID, What: d.What, Because: d.Because, Outward: d.Outward, Wait: d.Wait}); err != nil {
			fmt.Fprintf(io.Err, "baton: warning — could not queue it for the lead session yet (%v); baton queues it again when you end your turn.\n", err)
		}
		fmt.Fprintln(io.Out, "baton: "+decide.WorkerProposed(d.ID, d.Outward, wait, waitWhy))
		return 0
	}
	if d.Outward != "" {
		fmt.Fprintf(io.Out, "baton: proposal %s recorded. Now put it to the human: %s. Since %s, if nobody answers by %s, baton answers Not now for them: "+
			"%s %sDo nothing else until it is answered; then follow what baton tells you.\n",
			d.ID, q.Call(), d.Outward, d.Deadline.Local().Format("15:04"), decide.NotNowRule(), waitNote(wait, waitWhy))
		return 0
	}
	fmt.Fprintf(io.Out, "baton: proposal %s recorded. Now put it to the human: %s. If nobody answers by %s, baton picks Go ahead for them, and you do it. %s"+
		"Do nothing else until it is answered; then follow what baton tells you.\n", d.ID, q.Call(), d.Deadline.Local().Format("15:04"), waitNote(wait, waitWhy))
	return 0
}

// waitNote says why a proposal's wait is not the one the model asked for, with a space after it; "" when it is.
func waitNote(wait time.Duration, why string) string {
	if why == "" {
		return ""
	}
	return decide.WaitUsed(wait, why) + " "
}

// guard turns down a command the model could use to get out of a halt only the human may clear (a hard
// block, a proposal held for them, a review), or to move on before its proposal is put to the human.
// verb is how the refusal starts ("not done", "not recorded"). A command run outside the hosted session
// (isHosted false: hosted(io), read before the run's lock, as it reads processes) is the human's own, and is
// never held.
func guard(isHosted bool, st *state.State, verb string, proposalToo bool) error {
	if !isHosted {
		return nil
	}
	if what, since, held := st.Held(); held {
		return refusal{"held for the human", fmt.Sprintf("%s — the run is waiting on the human (%s, since %s), and only they can clear that. "+
			"End your turn now: baton has brought them in, and their answer will reach you here.", verb, what, since.Local().Format("15:04"))}
	}
	switch p := st.PendingProposal(); {
	case !proposalToo || p == nil:
	case p.Relayed:
		return refusal{"proposal pending", verb + " — " + decide.WorkerAwaits(p.ID, p.What)}
	case !p.Asked:
		return refusal{"proposal not asked", verb + " — " + decide.AskFirst(*p) + "."}
	}
	return nil
}

// pendingRefusal turns down a second proposal while one waits for its answer.
func pendingRefusal(p state.Decision) string {
	switch {
	case p.Relayed:
		return "not recorded — one proposal at a time: " + decide.WorkerAwaits(p.ID, p.What)
	case !p.Asked:
		return "not recorded — " + decide.AskFirst(p) + "."
	}
	return fmt.Sprintf("not recorded — your proposal %s (%s) is still waiting for the human's answer. One proposal at a time: wait for it, then follow what baton tells you.", p.ID, p.What)
}
