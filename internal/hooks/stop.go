package hooks

import (
	"fmt"
	"slices"
	"strings"
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
	s, err := h.d.Open(c)
	if err != nil {
		return Result{}, err
	}
	pl, _ := s.LoadPlan()
	var d decision
	var ann string
	_, _, err = h.update(c, func(st *state.State, s *state.Store) error {
		recordStop(st, c)
		ann = announced(st)
		dropped, _ := vanished(st, c.Now, false) // the turn ended with the proposal's question unanswered
		dropped = append(dropped, relayVanished(st, c.Now)...)
		// A worker's plan is the run's narrowed to its phase (plan.Only): the plan file is the lead's to
		// follow, and so is restarting on a newer baton.
		worker := s.Worker() != ""
		var drift, took string
		var ev *event
		if !worker {
			drift, ev, took = planDrift(st, s, pl, c.Now)
		}
		if ev != nil {
			dropped = append(dropped, *ev)
			pl, _ = s.LoadPlan()
		}
		d = decideStop(st, pl, c.Now, words(c, s), s.WorkerDecisions(*st))
		d.events = append(dropped, d.events...)
		if drift != "" {
			msg, _ := d.out()["systemMessage"].(string)
			if took != "" && d.queued("boundary") {
				// No phase was done: the phase the plan file's change let start is taken up.
				msg = boundaryLine(st, pl, took+" can start")
			}
			d.say(strings.TrimSpace(drift + "\n" + msg))
		}
		if !worker {
			if newer := newerBaton(st, c); newer != "" {
				msg, _ := d.out()["systemMessage"].(string)
				d.say(strings.TrimSpace(msg + "\n" + newer))
				d.emit("newer_baton", map[string]any{"version": st.Run.NewerNoted})
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	for _, e := range d.events {
		s.Event(e.kind, e.fields)
	}
	if d.relay != nil {
		if err := s.Relay(*d.relay); err != nil {
			return Result{}, err // the next stop queues it again
		}
	}
	for _, m := range d.mail {
		s.Event("message_delivered", map[string]any{"message": m.Kind, "via": "stop"})
	}
	msg, _ := d.out()["systemMessage"].(string)
	if d.deliver.any() {
		d.out()["reason"] = d.deliver.text(s)
		msg = lines(msg, d.deliver.says())
		d.deliver.event(s, "stop")
	}
	if msg = lines(ann, msg); msg != "" {
		d.say(msg)
	}
	if len(d.output) == 0 {
		d.output = nil
	}
	return Result{Output: d.output}, nil
}

type event struct {
	kind   string
	fields map[string]any
}

type decision struct {
	output  map[string]any
	events  []event
	deliver delivery        // results the stop is refused with; stop writes the reason from them
	mail    []state.Message // baton's messages the stop is refused with
	relay   *state.Relay    // a worker's question, for the lead session to put to the human
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

// queued reports whether the stop queued a compaction for reason.
func (d *decision) queued(reason string) bool {
	return slices.ContainsFunc(d.events, func(e event) bool { return e.kind == "compact_queued" && e.fields["reason"] == reason })
}

// decideStop is the Stop decision table. Rows are checked in order; the first that applies wins. workers
// is the decisions of the run's workers (Store.WorkerDecisions), which the plan-complete notice counts.
func decideStop(st *state.State, pl plan.Plan, now time.Time, w decide.Words, workers []state.Decision) decision {
	var d decision
	cur := phaseTitle(pl, st.Current)
	switch st.Mode {
	case state.ModeIdle, state.ModePaused:
		return d
	case state.ModeComplete:
		if len(st.Due()) > 0 {
			// The verification that completed the plan: the model hears it first, and sums the run up.
			d.deliver = take(st, now)
			d.refuse("")
			return d
		}
		if w.Worker && !st.Run.CompleteNotified {
			// A worker's plan is its phase (plan.Only): done, it is the run's to land, not a plan complete.
			st.Run.CompleteNotified = true
			phase := ""
			if len(pl.Phases) == 1 {
				phase = pl.Phases[0].ID
			}
			d.say(fmt.Sprintf("baton: %s done → baton lands its work on the run's branch", phaseTitle(pl, phase)))
			d.emit("worker_finished", map[string]any{"phase": phase, "decisions": len(decide.WithoutHuman(st.Decisions))})
			return d
		}
		if !st.Run.CompleteNotified {
			st.Run.CompleteNotified = true
			n := len(decide.WithoutHuman(st.Decisions)) + len(decide.WithoutHuman(workers))
			done := decide.PlanDone(len(pl.Phases), n)
			notice(st, "plan_complete", pl.Title+": "+done, now)
			d.say("baton: plan complete — " + done)
			d.emit("plan_complete", map[string]any{"decisions": n})
		}
		return d
	}
	if st.CheckpointAsked && !st.BoundaryOwed {
		st.CheckpointAsked, st.CheckpointOwed = false, true // the human asked for it; the model need not remember
	}
	switch p := st.OwnProposal(); {
	case st.Run.Compaction.InFlight():
		// A compaction is already queued or underway; let the turn end so the host can type it.
	case st.ReviewDue != "" && w.Worker:
		// A worker's review is put to the human by the lead session; the phase waits for it.
		st.Run.StopBlocks = 0
		relayed(st, &d, "review", "review the decisions "+st.ReviewDue+" made without you", state.Relay{Kind: state.RelayReview, Since: st.ReviewAt, Count: st.Unattended(st.ReviewDue)}, now)
	case st.ReviewDue != "":
		// Before the boundary: a phase that ended on its last decision without the human is reviewed
		// before the next one starts.
		st.Run.StopBlocks = 0
		q := decide.ReviewQuestion(st.ReviewDue, st.Unattended(st.ReviewDue))
		escalateAsking(st, &d, "review", "review the decisions "+st.ReviewDue+" made without you", q, now)
	case st.Replan != nil:
		// The human approved a plan mid-run, and the model stopped without asking what to do with it.
		st.Run.StopBlocks++
		if st.Run.StopBlocks <= MaxStopBlocks {
			d.refuse(NudgePrefix + " You stopped, but put baton's question about the plan the human approved to them first: " + replanQuestion(st.Replan).Call() + ". baton acts on the answer itself; then follow what it tells you.")
			d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks, "why": "approved plan not asked"})
			return d
		}
		st.Replan = nil
		escalate(st, &d, "stalled", fmt.Sprintf("the model stopped %d times without asking you about the plan you approved", st.Run.StopBlocks), now)
	case st.BoundaryOwed:
		queueCompaction(st, "boundary", now)
		st.Run.StopBlocks = 0
		d.say(boundaryLine(st, pl, "phase done"))
		d.emit("compact_queued", map[string]any{"reason": "boundary", "epoch": st.Run.Compaction.Epoch, "next": st.Current})
	case st.CheckpointOwed:
		queueCompaction(st, "checkpoint", now)
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: checkpoint → compacting, then %s continues", cur))
		d.emit("compact_queued", map[string]any{"reason": "checkpoint", "epoch": st.Run.Compaction.Epoch})
	case st.CheckpointDue:
		// The human resumed the run after the conversation grew while it was paused, and the model stopped
		// without recording where the phase stands. Asked enough times, baton compacts without the notes:
		// the phase goes on from a summary rather than from everything said while it was paused.
		st.Run.StopBlocks++
		if st.Run.StopBlocks <= MaxStopBlocks {
			d.refuse(NudgePrefix + " You stopped first. " + decide.CheckpointOnResume(st.Current))
			d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks, "why": "no checkpoint after resume"})
			return d
		}
		st.CheckpointDue = false
		queueCompaction(st, "checkpoint", now)
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: no checkpoint notes after resuming → compacting anyway, then %s continues", cur))
		d.emit("compact_queued", map[string]any{"reason": "checkpoint", "epoch": st.Run.Compaction.Epoch, "notes": false})
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
	case st.Blocked != nil && w.Worker:
		// A worker's block stops its phase alone, and is put to the human by the lead session.
		st.Run.StopBlocks = 0
		relayed(st, &d, "blocked", fmt.Sprintf("blocked on %s: %s", st.Current, st.Blocked.Reason), state.Relay{Kind: state.RelayBlocked, Since: st.Blocked.Since, Because: st.Blocked.Reason}, now)
	case st.Blocked != nil:
		escalate(st, &d, "blocked", fmt.Sprintf("blocked on %s: %s", st.Current, st.Blocked.Reason), now)
	case st.Run.WarnOwed && st.Run.WarnQuestion != "":
		// The model was asked to put the context question to the human and stopped without doing so.
		// Refused once: the context warning is advice, so a second stop goes on as it would have.
		st.Run.WarnOwed = false
		d.refuse(decide.ContextWarning(decide.Question{Text: st.Run.WarnQuestion, Options: decide.WarnOptions}.Call(), true))
		d.emit("stop_refused", map[string]any{"phase": st.Current, "why": "context question not asked"})
	case !w.Worker && (st.RelayDue() != nil || st.RelayUnasked() != nil):
		// A worker's question for the human, which only the lead can put to them: it goes first.
		relayStop(st, &d, now, w.Timeout)
	case len(st.Due()) > 0:
		// A job of the phase in hand finished: its result is what the model goes on with.
		st.Run.StopBlocks = 0
		d.deliver = take(st, now)
		d.refuse("")
	case len(st.Mail()) > 0:
		// baton has something for the model that no hook could tell it yet: it goes on with that.
		st.Run.StopBlocks = 0
		d.mail = state.TakeMail(st)
		var texts []string
		for _, m := range d.mail {
			texts = append(texts, m.Text)
		}
		d.refuse(strings.Join(texts, "\n\n"))
	case w.Worker && st.PendingProposal() != nil:
		// A worker's proposal, which the lead session puts to the human: the stop is the model's to make,
		// and their answer wakes it. It is queued again, in case its own queueing failed.
		rp := st.PendingProposal()
		st.Run.StopBlocks = 0
		d.relay = &state.Relay{Kind: state.RelayProposal, Decision: rp.ID, What: rp.What, Because: rp.Because, Untimed: rp.Untimed}
		d.say(fmt.Sprintf("baton: %s waits on the human's answer to proposal %s, which the lead session puts to them", cur, rp.ID))
		d.emit("idle_on_relay", map[string]any{"phase": st.Current, "id": rp.ID})
	case st.Current == "":
		// Every phase left waits for a job, or a gate in the working tree holds the next one, so there is
		// nothing to work on; baton takes the next phase up as soon as one can start.
		st.Run.StopBlocks = 0
		waiting := ""
		if jobs := st.Unfinished(); len(jobs) > 0 {
			waiting = waitingOn(jobs)
		}
		d.say(decide.NoPhaseYet(waiting, st.HereGate()))
	case st.CurrentJob() != nil:
		// The phase in hand waits on its job, and nothing else is owed: the session idles in place. The
		// result wakes it; if the wait runs long and another phase can go on, the host switches to that.
		st.Run.StopBlocks = 0
		j := st.CurrentJob()
		d.say(decide.GateWait(cur, *j))
		d.emit("idle_on_job", map[string]any{"phase": st.Current, "job": j.ID})
	case len(st.Run.BusyBackground()) > 0:
		// Background work of the model's own runs: a long command Claude Code moved there, a subagent.
		// The first stop on new work is refused once, since there is usually something to do meanwhile.
		// After that the stop is allowed: the work wakes the session when it ends, and the watchdog looks
		// in if it runs long.
		bg := st.Run.BusyBackground()
		if st.Run.Untold(bg) {
			st.Run.BackgroundTold = nil
			for _, t := range bg {
				st.Run.BackgroundTold = append(st.Run.BackgroundTold, t.ID)
			}
			d.refuse(w.BackgroundStop(backgroundWhat(bg)))
			d.emit("stop_refused", map[string]any{"phase": st.Current, "why": "background work", "tasks": len(bg)})
			return d
		}
		st.Run.StopBlocks = 0
		d.say(fmt.Sprintf("baton: %s waits on background work (%s), which wakes Claude when it ends", cur, backgroundWhat(bg)))
		d.emit("idle_on_background", map[string]any{"phase": st.Current, "tasks": len(bg)})
	default:
		// While a plan runs, a stop needs a status even when the human started the turn: every run
		// begins with a human "go". To talk without the plan, the human pauses baton.
		st.Run.StopBlocks++
		if st.Run.StopBlocks <= MaxStopBlocks {
			d.refuse(w.NoStatus(st.Current, cur))
			d.emit("stop_refused", map[string]any{"phase": st.Current, "count": st.Run.StopBlocks})
			return d
		}
		escalate(st, &d, "stalled", fmt.Sprintf("the model stopped %d times on %s without reporting status", st.Run.StopBlocks, st.Current), now)
	}
	return d
}

// backgroundWhat names background work: the first task, and how many more.
func backgroundWhat(bg []state.Task) string {
	what := bg[0].Description
	if what == "" {
		what = bg[0].Type
	}
	if len(bg) > 1 {
		what = fmt.Sprintf("%s, and %d more", what, len(bg)-1)
	}
	return what
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
	st.Run.Compaction.Queue(reason, now)
}

// boundaryLine tells the human what the boundary compaction a stop queued leads to: the phase in hand
// starts after it (or resumes, set aside before), once what came first (after) happened; before any
// phase has started, it compacts the conversation the plan was attached in.
func boundaryLine(st *state.State, pl plan.Plan, after string) string {
	cur := phaseTitle(pl, st.Current)
	if !started(st) {
		return fmt.Sprintf("baton: plan attached → compacting the conversation so far, then %s starts", cur)
	}
	verb := "starts"
	if ps := st.Phases[st.Current]; ps != nil && ps.Status == state.PhaseParked {
		verb = "resumes"
	}
	return fmt.Sprintf("baton: %s → compacting, then %s %s", after, cur, verb)
}

// started reports whether any phase of the run has started: before that, a boundary owed is the one
// between planning and the first phase.
func started(st *state.State) bool {
	for _, ps := range st.Phases {
		if ps.Status != state.PhasePending {
			return true
		}
	}
	return false
}

func phaseTitle(pl plan.Plan, id string) string {
	if i := pl.Index(id); i >= 0 {
		return id + " (" + pl.Phases[i].Title + ")"
	}
	return id
}
