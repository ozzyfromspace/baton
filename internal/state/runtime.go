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
	// Subagents counts subagents currently running.
	Subagents int `json:"subagents"`
	// Background is the in-flight background work Claude Code reported at the last Stop.
	Background []Task `json:"background,omitempty"`
	// Crons are session-scoped wakeups (CronCreate, ScheduleWakeup, /loop) reported at the last Stop.
	Crons int `json:"crons"`

	// StopBlocks counts consecutive Stops baton refused because the model reported no status.
	StopBlocks int `json:"stop_blocks"`

	Compaction Compaction `json:"compaction"`
	// Notices are messages for the human that the host sends out of band (push and desktop), then clears.
	Notices []Notice `json:"notices,omitempty"`
	// CompleteNotified is set once the human has been told the plan is complete.
	CompleteNotified bool        `json:"complete_notified,omitempty"`
	Context          *ContextUse `json:"context,omitempty"`
	Escalation       *Escalation `json:"escalation,omitempty"`
	LastError        *StopError  `json:"last_error,omitempty"`
	Ended            *Ended      `json:"ended,omitempty"`
}

// Dialog is an open prompt the human must answer.
type Dialog struct {
	Tool  string    `json:"tool"`
	Since time.Time `json:"since"`
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
}

// InFlight reports whether a baton-requested compaction is queued or underway.
func (c Compaction) InFlight() bool {
	return c.Status == CompactQueued || c.Status == CompactTyped || c.Status == CompactActive
}

// ContextUse is the context window fill reported by Claude Code's status line input.
type ContextUse struct {
	UsedPct    float64   `json:"used_pct"`
	WindowSize int       `json:"window_size,omitempty"`
	At         time.Time `json:"at"`
}

// Escalation records that baton brought the human in, and why.
type Escalation struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	Asked  bool      `json:"asked"` // the in-session question was requested
	Pushed bool      `json:"pushed"`
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
}

// Ended records the session ending.
type Ended struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// BusyBackground reports background work that can still wake the session: anything in flight except
// monitors, which watch indefinitely and must not make an idle session look busy.
func (r Runtime) BusyBackground() []Task {
	var out []Task
	for _, t := range r.Background {
		if t.Type != "monitor" {
			out = append(out, t)
		}
	}
	return out
}
