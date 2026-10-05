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

	// Dialog is set while Claude Code waits on the human: a permission prompt or a question.
	Dialog *Dialog `json:"dialog,omitempty"`
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
	ContextNudged   bool        `json:"context_nudged,omitempty"`
	ContextNudgedAt int         `json:"context_nudged_at,omitempty"`
	ContextWarned   bool        `json:"context_warned,omitempty"`
	Escalation      *Escalation `json:"escalation,omitempty"`
	LastError       *StopError  `json:"last_error,omitempty"`
	// PendingDone is the pending command (after elevation) already handed to the model.
	PendingDone string `json:"pending_done,omitempty"`
	Ended       *Ended `json:"ended,omitempty"`
}

// Dialog is an open prompt the human must answer.
type Dialog struct {
	Tool  string    `json:"tool"`
	Since time.Time `json:"since"`
	// Kind names a dialog baton opened itself: "context_warning" for the context question, which baton
	// answers itself ("Keep going") if nobody has within a while. AutoAnswered is when it did.
	Kind         string    `json:"kind,omitempty"`
	AutoAnswered time.Time `json:"auto_answered,omitzero"`
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
	Kind   string    `json:"kind,omitempty"` // blocked, stalled, stuck, draft, compaction_failed, api_error, …
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	Asked  bool      `json:"asked"` // the in-session question was requested
	Pushed bool      `json:"pushed"`
	// Watchdog escalations (raised by the host for inactivity) clear themselves once the session shows
	// signs of life again; escalations from the Stop table wait for the human's answer.
	Watchdog bool `json:"watchdog,omitempty"`
	// Reminded counts the reminders sent while the escalation stays unresolved; LastPush is the latest.
	Reminded int       `json:"reminded,omitempty"`
	LastPush time.Time `json:"last_push,omitzero"`
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

// Ended records the session ending.
type Ended struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Progress records that the plan moved or the human took part: escalations about the run standing
// still no longer apply, and the reminder count starts over.
func (r *Runtime) Progress() {
	r.IdleNudges, r.StopBlocks = 0, 0
	if e := r.Escalation; e != nil && e.Kind != "blocked" {
		r.Escalation = nil
	}
}

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
