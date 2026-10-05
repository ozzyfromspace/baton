package hooks

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// MaxStopBlocks is how many times in a row baton refuses a stop that came with no status before it
// stops asking the model and brings the human in.
const MaxStopBlocks = 3

// stop decides what happens when the model ends its turn. The decision depends only on recorded state
// (what the CLI and the other hooks wrote), never on what the model said.
func (h *handlers) stop(c Context) (Result, error) {
	s, err := h.d.Open(c.Env)
	if err != nil {
		return Result{}, err
	}
	pl, _ := s.LoadPlan()
	var d decision
	_, _, err = h.update(c, func(st *state.State, s *state.Store) error {
		recordStop(st, c)
		dropped, _ := vanished(st, c.Now, false) // the turn ended with the proposal's question unanswered
		d = decideStop(st, pl, c.Now, words(c, s))
		d.events = append(dropped, d.events...)
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	for _, e := range d.events {
		s.Event(e.kind, e.fields)
	}
	return Result{Output: d.output}, nil
}

type event struct {
	kind   string
	fields map[string]any
}

type decision struct {
	output map[string]any
	events []event
}

func (d *decision) say(msg string) { d.out()["systemMessage"] = msg }
func (d *decision) emit(kind string, fields map[string]any) {
	d.events = append(d.events, event{kind, fields})
}
func (d *decision) out() map[string]any {
	if d.output == nil {
		d.output = map[string]any{}
	}
	return d.output
}
func (d *decision) refuse(reason string) {
	d.out()["decision"] = "block"
	d.out()["reason"] = reason
}

// decideStop is the Stop decision table. Rows are checked in order; the first that applies wins.
func decideStop(st *state.State, pl plan.Plan, now time.Time, w decide.Words) decision {
	var d decision
	cur := phaseTitle(pl, st.Current)
	switch st.Mode {
	case state.ModeIdle, state.ModePaused:
		return d
	case state.ModeComplete:
		if !st.Run.CompleteNotified {
			st.Run.CompleteNotified = true
			notice(st, "plan_complete", fmt.Sprintf("%s: all %d phases done", pl.Title, len(pl.Phases)), now)
			d.say(fmt.Sprintf("baton: plan complete — all %d phases done", len(pl.Phases)))
			d.emit("plan_complete", nil)
		}
		return d
	}
	if st.CheckpointAsked && !st.BoundaryOwed {
		st.CheckpointAsked, st.CheckpointOwed = false, true // the human asked for it; the model need not remember
	}
	switch p := st.PendingProposal(); {
	case st.Run.Compaction.InFlight():
		// A compaction is already queued or underway; let the turn end so the host can type it.
	case st.ReviewDue != "":
		// Before the boundary: a phase that ended on its last decision without the human is reviewed
		// before the next one starts.
		st.Run.StopBlocks = 0
		q := decide.ReviewQuestion(st.ReviewDue, st.Unattended(st.ReviewDue))
		escalateAsking(st, &d, "review", "review the decisions "+st.ReviewDue+" made without you", q, now)
	case st.BoundaryOwed:
		queueCompaction(st, "boundary", now)
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: phase done → compacting, then %s starts", cur))
		d.emit("compact_queued", map[string]any{"reason": "boundary", "epoch": st.Run.Compaction.Epoch, "next": st.Current})
	case st.CheckpointOwed:
		queueCompaction(st, "checkpoint", now)
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: checkpoint → compacting, then %s continues", cur))
		d.emit("compact_queued", map[string]any{"reason": "checkpoint", "epoch": st.Run.Compaction.Epoch})
	case p != nil && !p.Asked:
		// The model proposed something and stopped without asking: nothing would ever answer it.
		st.Run.StopBlocks++
		if st.Run.StopBlocks <= MaxStopBlocks {
			d.refuse(NudgePrefix + " You stopped, but " + decide.AskFirst(p.ID, p.Question) + ". baton acts on the answer itself; then follow what it tells you.")
			d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks, "why": "proposal not asked"})
			return d
		}
		escalate(st, &d, "stalled", fmt.Sprintf("the model stopped %d times on %s without putting its proposal %s to you", st.Run.StopBlocks, st.Current, p.ID), now)
	case st.Run.Escalation != nil && st.Run.Escalation.Kind == "decision":
		// A proposal held for the human (Wait for me, or its question went away unanswered). They were
		// asked already, and pushed; their next message is the answer.
		st.Run.StopBlocks = 0
		d.say("baton: waiting on you — " + st.Run.Escalation.Reason)
	case st.Blocked != nil:
		escalate(st, &d, "blocked", fmt.Sprintf("blocked on %s: %s", st.Current, st.Blocked.Reason), now)
	case st.Waiting != nil && now.Before(st.Waiting.Until):
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: waiting for %s until %s", st.Waiting.What, st.Waiting.Until.Local().Format("15:04")))
	default:
		// While a plan runs, a stop needs a status even when the human started the turn: every run
		// begins with a human "go". To talk without the plan, the human pauses baton. Background work
		// is no excuse either: a dev server or a log watcher runs forever, so a stop that waits on it
		// must say for how long (baton waiting), and baton holds it to that.
		st.Run.StopBlocks++
		if st.Run.StopBlocks <= MaxStopBlocks {
			reason := w.NoStatus(st.Current, cur)
			if bg := st.Run.BusyBackground(); len(bg) > 0 {
				reason += " " + backgroundNote(bg)
			}
			d.refuse(reason)
			d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks, "background": len(st.Run.BusyBackground())})
			return d
		}
		escalate(st, &d, "stalled", fmt.Sprintf("the model stopped %d times on %s without reporting status", st.Run.StopBlocks, st.Current), now)
	}
	return d
}

func backgroundNote(bg []state.Task) string {
	what := bg[0].Description
	if what == "" {
		what = bg[0].Type
	}
	if len(bg) > 1 {
		what = fmt.Sprintf("%s, and %d more", what, len(bg)-1)
	}
	return fmt.Sprintf("Background work is still running (%s), but that is not a status: if you are waiting on it, use `baton waiting` with how long it should take, so baton can wake you if it never reports back.", what)
}

// escalate brings the human in: once in the session (a question, which reaches all their devices) and
// once out of band (the host sends a push). A repeated escalation for the same reason does neither again.
func escalate(st *state.State, d *decision, kind, reason string, now time.Time) {
	escalateAsking(st, d, kind, reason, decide.Question{Text: "baton: " + reason, Options: decide.EscalationOptions}, now)
}

// escalateAsking is escalate with the question to ask.
func escalateAsking(st *state.State, d *decision, kind, reason string, q decide.Question, now time.Time) {
	e := st.Run.Escalation
	if e == nil || e.Reason != reason {
		e = &state.Escalation{Kind: kind, Reason: reason, Since: now}
		st.Run.Escalation = e
		notice(st, kind, reason, now)
		d.emit("escalated", map[string]any{"type": kind, "reason": reason})
	}
	if e.Question == "" {
		e.Question = decide.Sanitize(q.Text)
	}
	if !e.Asked {
		e.Asked = true
		d.refuse(askReason(decide.Question{Text: e.Question, Options: q.Options}))
		return
	}
	d.say("baton: waiting on you — " + reason)
}

// askReason makes the model put a fixed question to the human. AskUserQuestion reaches every device the
// human uses, which is why baton routes the in-session escalation through it.
func askReason(q decide.Question) string {
	return "[baton] Bring the human in now: " + q.Call() + ". baton acts on the answer itself; then follow what it tells you."
}

func notice(st *state.State, kind, text string, now time.Time) {
	st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: kind, Text: text, At: now})
}

func queueCompaction(st *state.State, reason string, now time.Time) {
	c := &st.Run.Compaction
	*c = state.Compaction{Epoch: c.Epoch + 1, Status: state.CompactQueued, Reason: reason, Queued: now, Rewoken: c.Rewoken}
}

func phaseTitle(pl plan.Plan, id string) string {
	if i := pl.Index(id); i >= 0 {
		return id + " (" + pl.Phases[i].Title + ")"
	}
	return id
}
