package state

import "time"

// Runtime is what the hooks observe about the live session. The host's controller and the Stop
// decision table read it; nothing in it is ever inferred from the model's prose.
type Runtime struct {
	SessionID      string `json:"session_id,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`

	// TurnOpen is true between a prompt and its Stop. TurnBy says who started the turn: "human" for a
	// prompt typed at the keyboard, "baton" for a rewake or a nudge baton sent, "system" for other task
	// notifications (a background shell finishing).
	TurnOpen     bool      `json:"turn_open"`
	TurnBy       string    `json:"turn_by,omitempty"`
	TurnStarted  time.Time `json:"turn_started,omitzero"`
	LastStop     time.Time `json:"last_stop,omitzero"`
	LastActivity time.Time `json:"last_activity,omitzero"` // any hook from the main agent or a subagent
	// HumanAt is when the human last took part: a prompt they typed, or their answer to one of baton's
	// questions (not one baton answered for them).
	HumanAt time.Time `json:"human_at,omitzero"`

	// Dialogs are what Claude Code is waiting on the human for (permission prompts and questions), oldest
	// first. Claude Code shows them one at a time, in the order they were requested, and fires
	// PermissionRequest when one joins the queue, not when it reaches the screen
	// (docs/research/escalation.md). A state file from v0.1 has a single "dialog" instead; it is ignored.
	Dialogs DialogQueue `json:"dialogs,omitempty"`
	// Gate is why baton may not type right now, in its own words, or "" when it may. It exists so a
	// human can tell a blocked run from a working one: a queued compaction used to render on the status
	// line as "compacting…" however long it had been held, which claims something is happening.
	Gate string `json:"gate,omitempty"`
	// Subagents counts subagents currently running.
	Subagents int `json:"subagents"`
	// Background is the in-flight background work Claude Code reported at the last Stop.
	Background []Task `json:"background,omitempty"`
	// Crons are session-scoped wakeups (CronCreate, ScheduleWakeup, /loop) reported at the last Stop.
	Crons int `json:"crons"`

	// StopBlocks counts consecutive Stops baton refused because the model reported no status.
	StopBlocks int `json:"stop_blocks"`
	// IdleNudges counts the reminders baton typed since the plan last moved (a phase done, a checkpoint,
	// a block, the human taking part). Reminders that keep getting answered without progress escalate.
	IdleNudges int `json:"idle_nudges,omitempty"`

	Compaction Compaction `json:"compaction"`
	// Notices are messages for the human that the host sends out of band (push and desktop), then clears.
	Notices []Notice `json:"notices,omitempty"`
	// CompleteNotified is set once the human has been told the plan is complete.
	CompleteNotified bool        `json:"complete_notified,omitempty"`
	Context          *ContextUse `json:"context,omitempty"`
	// ContextNudged is set once the model has been asked to checkpoint (ContextNudgedAt: at what size, so
	// the request is repeated as the context keeps growing), and ContextWarned once the human has been
	// asked; each re-arms when the context falls well below its threshold (after a compaction).
	ContextNudged   bool `json:"context_nudged,omitempty"`
	ContextNudgedAt int  `json:"context_nudged_at,omitempty"`
	ContextWarned   bool `json:"context_warned,omitempty"`
	// WarnQuestion is the context question baton last issued, exactly as issued, until it is answered.
	WarnQuestion string      `json:"warn_question,omitempty"`
	Escalation   *Escalation `json:"escalation,omitempty"`
	LastError    *StopError  `json:"last_error,omitempty"`
	// PendingDone is the pending command (after elevation) already handed to the model.
	PendingDone string `json:"pending_done,omitempty"`
	// PlanFile is the plan file of the plan approval last asked for, and ClearedPlan a plan the human
	// approved with "clear context": Claude Code then fires no PostToolUse for it, only SessionEnd(clear)
	// with the approval still on screen (docs/research/sessions.md).
	PlanFile    string    `json:"plan_file,omitempty"`
	ClearedPlan *Approval `json:"cleared_plan,omitempty"`
	// ExitAt is when the human asked to leave baton (`baton exit`): once the turn ends, the host stops
	// claude, and the shell resumes the conversation as plain Claude Code.
	ExitAt time.Time `json:"exit_at,omitzero"`
	Ended  *Ended    `json:"ended,omitempty"`
}

// Dialog is an open prompt the human must answer.
type Dialog struct {
	Tool string `json:"tool"`
	// Agent is the subagent that asked ("" for the main agent). Key identifies the call itself, so its
	// result can close this entry and no other: hook inputs carry no tool_use_id before the result.
	Agent string    `json:"agent,omitempty"`
	Key   string    `json:"key,omitempty"`
	Since time.Time `json:"since"`
	// Kind names a question baton issued, put to the human word for word in the shape baton gave it:
	// "escalation", "review", or one baton answers itself if nobody has within a while: "context_warning"
	// (Keep going) and "proposal" (Go ahead, at its deadline). AutoAnswered is when it did.
	Kind         string    `json:"kind,omitempty"`
	AutoAnswered time.Time `json:"auto_answered,omitzero"`
}

// Same reports whether d and o are the same entry.
func (d Dialog) Same(o Dialog) bool {
	return d.Tool == o.Tool && d.Agent == o.Agent && d.Key == o.Key && d.Since.Equal(o.Since)
}

// DialogQueue is the open dialogs, oldest first.
//
// An entry closes only when its own call's result arrives (CloseExact), or when something proves no
// dialog of its owner can still be open: that agent ended (CloseAgent), the main turn ended
// (CloseMain), or Claude Code reported the session idle (Clear). A denied permission prompt fires no
// hook at all, so entries can go stale. A stale entry only ever holds baton back: it keeps the typing
// gate shut, and keeps baton's own question from being the only dialog open.
type DialogQueue []Dialog

// Open adds a dialog at the back of the queue.
func (q *DialogQueue) Open(d Dialog) { *q = append(*q, d) }

// CloseExact closes the oldest dialog that agent opened for exactly this tool and key, and returns it.
// There is deliberately no nearest match: a call that never had a dialog must not close a live one.
func (q *DialogQueue) CloseExact(agent, tool, key string) (Dialog, bool) {
	for i, d := range *q {
		if d.Agent == agent && d.Tool == tool && d.Key == key {
			*q = append((*q)[:i:i], (*q)[i+1:]...)
			return d, true
		}
	}
	return Dialog{}, false
}

// CloseAgent closes every dialog a subagent opened (it has stopped).
func (q *DialogQueue) CloseAgent(agent string) {
	if agent != "" {
		q.remove(func(d Dialog) bool { return d.Agent == agent })
	}
}

// CloseMain closes every dialog the main agent opened (its turn has ended).
func (q *DialogQueue) CloseMain() { q.remove(func(d Dialog) bool { return d.Agent == "" }) }

// Clear closes every dialog.
func (q *DialogQueue) Clear() { *q = nil }

func (q *DialogQueue) remove(drop func(Dialog) bool) {
	var kept DialogQueue
	for _, d := range *q {
		if !drop(d) {
			kept = append(kept, d)
		}
	}
	*q = kept
}

// AnyOpen reports whether any dialog is open.
func (q DialogQueue) AnyOpen() bool { return len(q) > 0 }

// Has reports whether a dialog of this kind is open.
func (q DialogQueue) Has(kind string) bool {
	for _, d := range q {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

// Front is the oldest open dialog: the one Claude Code shows first.
func (q DialogQueue) Front() (Dialog, bool) {
	if len(q) == 0 {
		return Dialog{}, false
	}
	return q[0], true
}

// Only is the open dialog when exactly one is open. Only then can baton know which dialog a key it
// types lands in.
func (q DialogQueue) Only() (Dialog, bool) {
	if len(q) != 1 {
		return Dialog{}, false
	}
	return q[0], true
}

// Task is one piece of in-flight background work.
type Task struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // shell, subagent, monitor, workflow, …
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

// Compaction statuses, in order.
const (
	CompactQueued = "queued" // baton decided to compact; the host types /compact when it is safe
	CompactTyped  = "typed"  // the host typed it; waiting for PreCompact
	CompactActive = "active" // PreCompact fired: Claude Code is summarizing
	CompactDone   = "done"   // PostCompact fired
	CompactFailed = "failed" // no acknowledgement or completion in time; escalated
)

// Compaction tracks the compaction baton asked for (if any) and the last one that happened.
type Compaction struct {
	Epoch    int       `json:"epoch"`
	Status   string    `json:"status,omitempty"`
	Reason   string    `json:"reason,omitempty"` // "boundary" or "checkpoint"
	Tries    int       `json:"tries,omitempty"`
	Queued   time.Time `json:"queued,omitzero"`
	Typed    time.Time `json:"typed,omitzero"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Trigger and ByBaton describe the compaction that last started: "manual" or "auto", and whether
	// it was the one baton requested.
	Trigger string `json:"trigger,omitempty"`
	ByBaton bool   `json:"by_baton,omitempty"`
	// Rewoken is the last epoch whose completion woke the model, so each compaction wakes it once.
	Rewoken int `json:"rewoken,omitempty"`
	// Nudged is when baton typed a reminder because the model did not resume after this compaction.
	Nudged time.Time `json:"nudged,omitzero"`
	// FailedAt and Rounds: when baton last gave up on this compaction, and how many times it started
	// over. A failed compaction is retried with backoff, never abandoned while one is owed.
	FailedAt time.Time `json:"failed_at,omitzero"`
	Rounds   int       `json:"rounds,omitempty"`
}

// InFlight reports whether a baton-requested compaction is queued or underway.
func (c Compaction) InFlight() bool {
	return c.Status == CompactQueued || c.Status == CompactTyped || c.Status == CompactActive
}

// ContextUse is the context window fill reported by Claude Code's status line input.
type ContextUse struct {
	UsedPct    float64 `json:"used_pct"`              // percent of the model's window, as Claude Code reports it
	Tokens     int     `json:"tokens,omitempty"`      // the size of the last request: its input, cached or not
	WindowSize int     `json:"window_size,omitempty"` // the model's context window
	// Limit is the compaction window: the model's window, or the --autocompact cap when that is smaller.
	Limit int       `json:"limit,omitempty"`
	At    time.Time `json:"at"`
}

// Used is the context size in tokens, estimated from the percentage when the exact count is missing.
func (c ContextUse) Used() int {
	if c.Tokens > 0 {
		return c.Tokens
	}
	return int(c.UsedPct * float64(c.WindowSize) / 100)
}

// Escalation records that baton brought the human in, and why.
type Escalation struct {
	Kind   string `json:"kind,omitempty"` // blocked, stalled, stuck, draft, compaction_failed, api_error, …
	Reason string `json:"reason"`
	// Question is the question baton told the model to put to the human, exactly as issued.
	Question string    `json:"question,omitempty"`
	Since    time.Time `json:"since"`
	Asked    bool      `json:"asked"` // the in-session question was requested
	Pushed   bool      `json:"pushed"`
	// Watchdog escalations (raised by the host for inactivity) clear themselves once the session shows
	// signs of life again; escalations from the Stop table wait for the human's answer.
	Watchdog bool `json:"watchdog,omitempty"`
	// Reminded counts the reminders sent while the escalation stays unresolved; LastPush is the latest.
	Reminded int       `json:"reminded,omitempty"`
	LastPush time.Time `json:"last_push,omitzero"`
	// Snapshot is the ref holding the uncommitted work the host saved when the run halted for this.
	Snapshot string `json:"snapshot,omitempty"`
}

// StopError is the last API error that ended a turn (rate limit, overload, …).
type StopError struct {
	Error   string    `json:"error"`
	Details string    `json:"details,omitempty"`
	At      time.Time `json:"at"`
}

// Notice is one message for the human, sent outside the session.
type Notice struct {
	Kind string    `json:"kind"` // blocked, stalled, compaction_failed, plan_complete, …
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Tries and NextTry: a notice whose delivery failed stays queued and is retried.
	Tries   int       `json:"tries,omitempty"`
	NextTry time.Time `json:"next_try,omitzero"`
}

// Approval is a plan the human approved, by its file.
type Approval struct {
	File string    `json:"file"`
	At   time.Time `json:"at"`
}

// Ended records the session ending.
type Ended struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Progress records that the plan moved or the human took part: escalations about the run standing
// still no longer apply, and the reminder count starts over. Escalations that wait on the human's
// decision stay: the model reporting progress must not be able to dismiss them.
func (r *Runtime) Progress() {
	r.IdleNudges, r.StopBlocks = 0, 0
	if e := r.Escalation; e != nil && !humanDecides[e.Kind] {
		r.Escalation = nil
	}
}

// humanDecides are the escalations only the human can resolve: a hard block, a proposal held for
// them (decision), and a review of the decisions made without them.
var humanDecides = map[string]bool{"blocked": true, "decision": true, "review": true}

// waitable are the background task types (as Claude Code reports them at Stop) that are the model's own
// work and wake the session when they finish. Monitors watch indefinitely, and dream, auto-mode scan
// and memory import are Claude Code's own housekeeping: none of those may make an idle session look busy.
var waitable = map[string]bool{"shell": true, "subagent": true, "workflow": true, "MCP task": true, "teammate": true, "cloud session": true}

// BusyBackground reports background work that can still wake the session.
func (r Runtime) BusyBackground() []Task {
	var out []Task
	for _, t := range r.Background {
		if waitable[t.Type] {
			out = append(out, t)
		}
	}
	return out
}
