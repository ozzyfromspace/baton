package state

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
)

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
	// History says the conversation has turns before the current one: a turn ended in this session, or
	// the session resumed an earlier conversation. A plan attached then starts after a compaction, from a
	// brief, rather than in the middle of whatever came before.
	History bool `json:"history,omitempty"`
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
	// BackgroundTold are the ids of the background work a stop was last refused over, so the model has
	// been told to keep working meanwhile: another stop on that work is allowed.
	BackgroundTold []string `json:"background_told,omitempty"`
	// Monitors are the task ids of the Monitor watchers the session armed (PostToolUse(Monitor)
	// tool_response.taskId), while they still run. Claude Code reports a Monitor at Stop as type "shell",
	// like a background command, so its id is the only way to tell the two apart (spike 19).
	Monitors []string `json:"monitors,omitempty"`
	// Crons are session-scoped wakeups (CronCreate, ScheduleWakeup, /loop) reported at the last Stop.
	Crons int `json:"crons"`
	// Tools are the tool calls whose results have not come back yet, oldest first: each may be changing
	// files right now, so no worker's work lands in the checkout while one is (ToolCall).
	Tools []ToolCall `json:"tools,omitempty"`
	// Spawning is the phase agent baton's mod is starting in the lead's claude, from its call's PreToolUse
	// until its worktree is made (WorktreeCreate) or the call ends (Spawning).
	Spawning *Spawning `json:"spawning,omitempty"`
	// AgentCalls are the Agent tool's calls of the model's own in the lead's claude (the lead's, or any
	// agent's), oldest first, whose agents may still ask for a worktree (AgentCall).
	AgentCalls []AgentCall `json:"agent_calls,omitempty"`
	// Plain are the worktrees baton made in a hosted session for an agent that is not one of its phases
	// (the model's own isolated subagents, a workflow's agents), where Claude Code would have, newest last:
	// one still as it was made when its agent stops is removed, as Claude Code removes its own (PlainTree).
	Plain []PlainTree `json:"plain,omitempty"`
	// Briefed is, in a phase's sub-run, the phase agent its brief was given to (SubagentStart): a message
	// starts an idle agent again with its conversation, and it is not briefed again; another agent on the
	// phase is.
	Briefed string `json:"briefed,omitempty"`
	// Transcript is, in a phase's sub-run, the briefed agent's own transcript (subagents/agent-<id>.jsonl
	// beside the lead's): its hooks carry the lead's transcript_path, so its context and its compactions
	// are read there, by its loop (Fresh, AgentBoundary).
	Transcript string `json:"transcript,omitempty"`

	// StopBlocks counts consecutive Stops baton refused because the model reported no status.
	StopBlocks int `json:"stop_blocks"`
	// IdleNudges counts the reminders baton typed since the plan last moved (a phase done, a checkpoint,
	// a block, the human taking part). Reminders that keep getting answered without progress escalate.
	IdleNudges int `json:"idle_nudges,omitempty"`

	Compaction Compaction `json:"compaction"`
	// ModReports is when baton's mod last reported a compaction of the lead begun (Report). Where the mod
	// runs, it reports each one as it begins, so a /compact baton typed that it says nothing of soon did
	// not take; where it does not (this claude has not shown it yet), only the compaction's proof can say.
	ModReports time.Time `json:"mod_reports,omitzero"`
	// Serial is why the host that decided it runs the plan's phases one at a time in the lead: baton's mod
	// did not say hello in its claude, or is another baton's (loop/mod.go). Another host decides afresh.
	Serial *Serial `json:"serial,omitempty"`
	// Proven is where the lead's transcript stood when baton last knew of every compaction in it: a compact
	// boundary past it is a compaction baton has not counted yet (Boundary). nil until a hook of the lead's
	// own marks it (Remark).
	Proven *Mark `json:"proven,omitempty"`
	// Briefing is the brief a compaction proven over owes the model, until it is given (Compacted).
	Briefing *Briefing `json:"briefing,omitempty"`
	// Notices are messages for the human that the host sends out of band (push and desktop), then clears.
	Notices []Notice `json:"notices,omitempty"`
	// Stalled is the host's last notice that the run as a whole stopped moving (loop's stall alarm), until
	// the run moves again. It is no escalation: nothing waits on it.
	Stalled *Stall `json:"stalled,omitempty"`
	// Announce are lines for the human about what the host did on its own, such as a verification it
	// settled: the host cannot write into the session, so the next hook that answers it shows them.
	Announce []string `json:"announce,omitempty"`
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
	// WarnOwed is set while it has not been put on screen: a stop is refused once over it, and a
	// compaction makes it moot.
	// DirectivesAsked lists the plan files whose first ExitPlanMode baton answered with its request to flag
	// the instructions that compete with the plan (once each).
	DirectivesAsked []string `json:"directives_asked,omitempty"`
	// DirectivesPending is the plan file asked about whose ExitPlanMode has not come again yet: a stop in
	// plan mode before it is turned back once, so the plan still reaches the human.
	DirectivesPending string      `json:"directives_pending,omitempty"`
	WarnQuestion      string      `json:"warn_question,omitempty"`
	WarnOwed          bool        `json:"warn_owed,omitempty"`
	Escalation        *Escalation `json:"escalation,omitempty"`
	LastError         *StopError  `json:"last_error,omitempty"`
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
	// Restart is the newer baton the host is restarting this session on, until the restarted session
	// has said so; NewerNoted is the newer installed baton the human was last told about.
	Restart    *Restart `json:"restart,omitempty"`
	NewerNoted string   `json:"newer_noted,omitempty"`
	// StaleSkill is the version a session restarted from while idle: /baton instructions loaded earlier
	// in the conversation are that version's, and the next prompt is handed the current ones.
	StaleSkill string `json:"stale_skill,omitempty"`
	Ended      *Ended `json:"ended,omitempty"`
	// Mark is where the session's transcript stood when the last prompt was submitted, until a hook of its
	// turn has seen whether the prompt goes on from there: one that does not came after a rewind
	// (docs/research/recovery.md).
	Mark *Mark `json:"mark,omitempty"`
	// Recoveries are the recent times the host brought the lead back after its claude exited by itself
	// mid-plan, oldest first; the host forgets the old ones, and counts these against its cap. FreshFor
	// is the phase it last started a fresh session for ("" for none in hand), when the conversation could
	// not be resumed: once per phase. nil: none yet.
	Recoveries []Recovery `json:"recoveries,omitempty"`
	FreshFor   *string    `json:"fresh_for,omitempty"`
}

// Stall is the notice that the run stopped moving (Runtime.Stalled): pushed At, for a run that had not
// moved since Since. The run moving after Since arms the alarm again.
type Stall struct {
	Since time.Time `json:"since"`
	At    time.Time `json:"at"`
}

// Serial is a host's decision to run the plan's phases one at a time in the lead (Runtime.Serial): the
// host instance that made it, why, and when.
type Serial struct {
	Instance string    `json:"instance"`
	Why      string    `json:"why"`
	At       time.Time `json:"at"`
}

// SerialWhy is why the phases of the run go one at a time in the lead, as the host that owns it decided,
// or "" when it decided no such thing.
func (st State) SerialWhy() string {
	if s := st.Run.Serial; s != nil && st.Owner != nil && st.Owner.Instance == s.Instance {
		return s.Why
	}
	return ""
}

// Briefing is a brief baton owes the model after a compaction: of its kind (the brief package's: boundary,
// switch, resume, checkpoint, auto), for phase, with the finished job a resumed phase was set aside for
// (Job). Wake says the compaction was baton's: the rewake wakes the model with the brief. Any other is
// given with the next tool call or prompt, since the conversation goes on by itself (or the human goes on
// with it).
type Briefing struct {
	Kind  string    `json:"kind"`
	Phase string    `json:"phase"`
	Job   string    `json:"job,omitempty"`
	Wake  bool      `json:"wake,omitempty"`
	At    time.Time `json:"at"`
}

// Mark is where a session's transcript stood when a prompt was submitted (transcript.Mark): the file, its
// size, and the uuid of its last entry in the conversation.
type Mark struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Leaf   string `json:"leaf"`
}

// Recovery is one time the host brought the lead back after its claude exited by itself mid-plan
// (docs/research/recovery.md): it resumed the conversation, or, when that could not be done, started a
// fresh session from the phase's brief.
type Recovery struct {
	At    time.Time `json:"at"`
	Why   string    `json:"why"` // "claude exited with code 137"
	Fresh bool      `json:"fresh,omitempty"`
	// Told: the session it brought back has been told, once (the resumed model woken, the fresh one briefed).
	Told bool `json:"told,omitempty"`
}

// Recovering is the latest recovery, if it is of the kind fresh says and the session it brought back has
// not been told of it yet; nil otherwise.
func (r *Runtime) Recovering(fresh bool) *Recovery {
	if n := len(r.Recoveries); n > 0 && r.Recoveries[n-1].Fresh == fresh && !r.Recoveries[n-1].Told {
		return &r.Recoveries[n-1]
	}
	return nil
}

// Restart is a host restarting its session on another version of baton (package upgrade).
type Restart struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// Dialog is an open prompt the human must answer.
type Dialog struct {
	Tool string `json:"tool"`
	// Agent is the subagent that asked ("" for the main agent). Key identifies the call itself, so its
	// result can close this entry and no other: hook inputs carry no tool_use_id before the result.
	Agent string    `json:"agent,omitempty"`
	Key   string    `json:"key,omitempty"`
	Since time.Time `json:"since"`
	// Phase is the phase whose phase agent asked: its dialog shows on the lead's screen, and is recorded in
	// the lead's run. Where baton's mod runs, one holds only that agent (LeadOpen).
	Phase string `json:"phase,omitempty"`
	// Kind names a question baton issued, put to the human word for word in the shape baton gave it:
	// "escalation", "review", or one baton answers itself if nobody has within a while: "context_warning"
	// (Keep going) and "proposal" (Go ahead, at its deadline). AutoAnswered is when it did.
	Kind         string    `json:"kind,omitempty"`
	AutoAnswered time.Time `json:"auto_answered,omitzero"`
	// Front is when baton first saw it at the front of the queue, on screen (hooks.Bound).
	Front time.Time `json:"front,omitzero"`
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

// LeadOpen reports whether a dialog is open that is not a phase agent's: the lead's own, or one of an
// agent of the model's own. Where baton's mod runs, only such a dialog holds the lead: the mod's
// compaction, the rewake after it and a line it submits all run under a phase agent's permission prompt,
// which survives them (spike 36).
func (q DialogQueue) LeadOpen() bool {
	return slices.ContainsFunc(q, func(d Dialog) bool { return d.Phase == "" })
}

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
	Type        string `json:"type"` // shell (a Monitor too), subagent, workflow, …
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	// AgentType is a subagent's type: PhaseAgent for one of baton's own phase agents.
	AgentType string `json:"agent_type,omitempty"`
	// Since is when a Stop first reported the task: Claude Code does not say when it started.
	Since time.Time `json:"since,omitzero"`
}

// Compaction statuses, in order.
const (
	CompactQueued = "queued" // baton decided to compact; the host asks for it (the mod, or /compact typed) when it is safe
	CompactTyped  = "typed"  // the host asked; waiting for proof it has begun (the mod's) or is over
	CompactActive = "active" // proven begun: Claude Code is summarizing
	CompactDone   = "done"   // proven over
	CompactFailed = "failed" // no acknowledgement or completion in time; escalated
	// CompactCancelled: a switch called off before its compaction was typed, as the job it was for ended
	// (Unswitch), or a boundary a phase's sub-run owed, which nothing compacts (Retake).
	CompactCancelled = "cancelled"
)

// Compaction tracks the compaction baton asked for (if any) and the last one that happened.
type Compaction struct {
	Epoch    int       `json:"epoch"`
	Status   string    `json:"status,omitempty"`
	Reason   string    `json:"reason,omitempty"` // "boundary", "checkpoint" or "switch"
	Tries    int       `json:"tries,omitempty"`
	Queued   time.Time `json:"queued,omitzero"`
	Typed    time.Time `json:"typed,omitzero"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Trigger and ByBaton describe the compaction that last started: "manual" or "auto", and whether
	// it was the one baton requested.
	Trigger string `json:"trigger,omitempty"`
	ByBaton bool   `json:"by_baton,omitempty"`
	// Reported is the mod's report of the lead's compaction over, kept for the rewake (state.Report).
	Reported *ModReport `json:"reported,omitempty"`
	// Command is the bridge command baton's mod was last given to run this compaction (0: /compact was
	// typed, or nothing was asked yet), and Declined why a try did not compact the lead: the mod's call
	// failed, a turn was running, or a hook vetoed it (state.Declined). The host asks again (Refused).
	Command  int      `json:"command,omitempty"`
	Declined *Decline `json:"declined,omitempty"`
	// Nudged is when baton typed a reminder because the model did not resume after this compaction.
	Nudged time.Time `json:"nudged,omitzero"`
	// WaitTold says the model was told, in the turn this compaction waits for (State.BoundaryWaits), that
	// the phase it takes up comes after that turn.
	WaitTold bool `json:"wait_told,omitempty"`
	// FailedAt and Rounds: when baton last gave up on this compaction, and how many times it started
	// over. A failed compaction is retried with backoff, never abandoned while one is owed.
	FailedAt time.Time `json:"failed_at,omitzero"`
	Rounds   int       `json:"rounds,omitempty"`
	// Switch is the switch the host made, for a switch it queued (Switch, Unswitch).
	Switch *Switched `json:"switch,omitempty"`
}

// Decline is why a try at a compaction did not compact the lead (state.Declined).
type Decline struct {
	Command int       `json:"command,omitempty"` // the bridge command of the mod's try (0: a /compact a hook vetoed)
	Why     string    `json:"why"`
	At      time.Time `json:"at"`
}

// Refused is why the latest try at this compaction did not compact the lead ("" while nothing says so): a
// decline of an earlier try, which the host has asked again since, is not this one's.
func (c Compaction) Refused() string {
	if d := c.Declined; d != nil && (d.Command == 0 || d.Command == c.Command) {
		return d.Why
	}
	return ""
}

// Queue asks the host to compact, for reason ("boundary", "checkpoint" or "switch"), as a new epoch.
func (c *Compaction) Queue(reason string, now time.Time) {
	*c = Compaction{Epoch: c.Epoch + 1, Status: CompactQueued, Reason: reason, Queued: now}
}

// StartsPhase reports a compaction after which another phase is taken up: a boundary, or a switch.
func (c Compaction) StartsPhase() bool { return c.Reason == "boundary" || c.Reason == "switch" }

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
	Kind   string `json:"kind,omitempty"` // blocked, stalled, stuck, draft, compaction_failed, api_error, refused, …
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

// StopError is the last API error that ended a turn (rate limit, overload, a refusal, …).
type StopError struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
	// Category is a refusal's, as the API named it ("cyber", …; "" for none).
	Category string    `json:"category,omitempty"`
	At       time.Time `json:"at"`
}

// ErrRefusal is the error of a turn the model's safeguards declined, which Claude Code would not go on
// with. StopFailure says invalid_request; the transcript says it was a refusal.
const ErrRefusal = "refusal"

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

// Deliberate reports a session the human chose to end: /exit, /clear, /logout (SessionEnd's reasons).
func (e *Ended) Deliberate() bool {
	return e != nil && (e.Reason == "prompt_input_exit" || e.Reason == "clear" || e.Reason == "logout")
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
// work and wake the session when they finish. Dream, auto-mode scan and memory import are Claude Code's
// own housekeeping, and Monitors (reported as "shell") watch indefinitely: none of those may make an idle
// session look busy.
var waitable = map[string]bool{"shell": true, "subagent": true, "workflow": true, "MCP task": true, "teammate": true, "cloud session": true}

// PhaseAgent is the agent type of baton's phase agents, which the mod in baton's plugin registers and
// spawns (docs/research/visible-workers.md).
const PhaseAgent = "baton:phase"

// BusyBackground reports background work that can still wake the session. baton's own phase agents are
// not the session's work: their reports never reach its model, and each phase has a loop of its own.
func (r Runtime) BusyBackground() []Task {
	var out []Task
	for _, t := range r.Background {
		if waitable[t.Type] && t.AgentType != PhaseAgent && !slices.Contains(r.Monitors, t.ID) {
			out = append(out, t)
		}
	}
	return out
}

// PhaseAgents reports baton's phase agents among the background work: they live in the session's claude,
// and end with it.
func (r Runtime) PhaseAgents() []Task {
	var out []Task
	for _, t := range r.Background {
		if t.AgentType == PhaseAgent {
			out = append(out, t)
		}
	}
	return out
}

// Untold reports whether bg holds work the model has not been told about at a stop: work that started
// since the last stop refused over background work.
func (r Runtime) Untold(bg []Task) bool {
	return slices.ContainsFunc(bg, func(t Task) bool { return !slices.Contains(r.BackgroundTold, t.ID) })
}

// Oldest is when the longest-running of tasks was first reported (zero for none).
func Oldest(tasks []Task) time.Time {
	var out time.Time
	for _, t := range tasks {
		if out.IsZero() || t.Since.Before(out) {
			out = t.Since
		}
	}
	return out
}

// Watching reports the Monitors still running. They are not work, but they live inside the claude
// process and end with it.
func (r Runtime) Watching() []Task {
	var out []Task
	for _, t := range r.Background {
		if slices.Contains(r.Monitors, t.ID) {
			out = append(out, t)
		}
	}
	return out
}

// Watch records a Monitor the session armed.
func (r *Runtime) Watch(id string) {
	if id != "" && !slices.Contains(r.Monitors, id) {
		r.Monitors = append(r.Monitors, id)
	}
}

// SetBackground records the background work a Stop reported at now, keeping when each task was first
// reported, and forgets the Monitors that have ended: a Monitor's PostToolUse always comes before the
// Stop of its turn, so one missing from the list is gone.
func (r *Runtime) SetBackground(tasks []Task, now time.Time) {
	for i := range tasks {
		tasks[i].Since = now
		if j := slices.IndexFunc(r.Background, func(t Task) bool { return t.ID == tasks[i].ID }); j >= 0 && !r.Background[j].Since.IsZero() {
			tasks[i].Since = r.Background[j].Since
		}
	}
	r.Background = tasks
	r.Monitors = slices.DeleteFunc(r.Monitors, func(id string) bool {
		return !slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id })
	})
}

// Spawning is the call baton's mod makes in the lead's session to start the phase agent on Phase: the Agent
// tool's call ToolUse, made at At, as PreToolUse saw it. Claude Code says neither the agent's type nor
// the call at WorktreeCreate (its prompt id is the session's latest prompt's, whoever's: a prompt of the
// lead's or another call of the mod's in between changes it), so a worktree asked for while this call is
// open is the phase agent's only when no call of the model's own may ask for one too (AgentCalls).
type Spawning struct {
	Phase   string    `json:"phase"`
	ToolUse string    `json:"tool_use"`
	At      time.Time `json:"at"`
}

// An AgentCall is a call of the model's own to the Agent tool (ToolUse), made at At by Agent ("" for the
// lead's main agent), whose agent may still ask for a worktree: Claude Code asks for an isolated agent's
// as the call starts, right after its PreToolUse, or, where the human must allow the call first, once they
// have. It is settled by its result, by a worktree baton makes for an agent of the model's own, or
// AgentCallWait after At while no Agent call of the same agent waits on the human.
type AgentCall struct {
	ToolUse string    `json:"tool_use"`
	Agent   string    `json:"agent,omitempty"`
	At      time.Time `json:"at"`
}

// AgentCallWait is how long after its PreToolUse an Agent call of the model's own, with no permission
// prompt open, may still ask for a worktree.
const AgentCallWait = 30 * time.Second

// maxAgentCalls bounds the Agent calls on record: one whose result never came must not pile up.
const maxAgentCalls = 32

// AgentCalled records the model's own Agent call id, made by agent at at, and lets go of the ones settled
// by time.
func (r *Runtime) AgentCalled(id, agent string, at time.Time) {
	r.AgentCalls = append(slices.DeleteFunc(r.AgentCalls, func(c AgentCall) bool { return c.ToolUse == id || !r.agentMayAsk(c, at) }),
		AgentCall{ToolUse: id, Agent: agent, At: at})
	if n := len(r.AgentCalls) - maxAgentCalls; n > 0 {
		r.AgentCalls = slices.Delete(r.AgentCalls, 0, n)
	}
}

// AgentCallEnded settles the model's own Agent call id: its result came.
func (r *Runtime) AgentCallEnded(id string) {
	r.AgentCalls = slices.DeleteFunc(r.AgentCalls, func(c AgentCall) bool { return c.ToolUse == id })
}

// AgentPlaced settles the oldest of the model's own Agent calls: baton made a worktree for its agent.
func (r *Runtime) AgentPlaced() {
	if len(r.AgentCalls) > 0 {
		r.AgentCalls = slices.Delete(r.AgentCalls, 0, 1)
	}
}

// AgentCallsOpen reports that a call of the model's own to the Agent tool may still ask for a worktree at
// now.
func (r Runtime) AgentCallsOpen(now time.Time) bool {
	return slices.ContainsFunc(r.AgentCalls, func(c AgentCall) bool { return r.agentMayAsk(c, now) })
}

// agentMayAsk reports that the agent of call c may still ask for a worktree at now: not long after its
// call, or with an Agent call's permission prompt of the same agent open. A permission prompt names the
// agent that asked (spike 36), so one agent's prompt keeps no other agent's call open; but a worktree
// request names nobody, so a call waiting on its prompt holds every phase agent's start until it is
// answered, lest the human allow it while baton's spawn is open.
func (r Runtime) agentMayAsk(c AgentCall, now time.Time) bool {
	return now.Sub(c.At) < AgentCallWait || slices.ContainsFunc(r.Dialogs, func(d Dialog) bool { return d.Tool == "Agent" && d.Agent == c.Agent })
}

// PlainTree is a worktree baton made for an agent of the model's own, named Name (agent-<id> for an
// isolated subagent), at Path on Branch from commit Base.
type PlainTree struct {
	Name   string    `json:"name,omitempty"`
	Path   string    `json:"path"`
	Branch string    `json:"branch"`
	Base   string    `json:"base"`
	At     time.Time `json:"at"`
}

// maxPlain bounds the plain worktrees on record: one whose agent never stopped must not pile up.
const maxPlain = 32

// MadePlain records a plain worktree baton made.
func (r *Runtime) MadePlain(t PlainTree) {
	r.Plain = append(slices.DeleteFunc(r.Plain, func(o PlainTree) bool { return o.Path == t.Path }), t)
	if n := len(r.Plain) - maxPlain; n > 0 {
		r.Plain = slices.Delete(r.Plain, 0, n)
	}
}

// PlainNamed is the plain worktree baton made with name, and lets go of it.
func (r *Runtime) PlainNamed(name string) (PlainTree, bool) {
	for i, t := range r.Plain {
		if name != "" && t.Name == name {
			r.Plain = slices.Delete(r.Plain, i, i+1)
			return t, true
		}
	}
	return PlainTree{}, false
}

// PlainAt is the plain worktree baton made that holds dir, and lets go of it.
func (r *Runtime) PlainAt(dir string) (PlainTree, bool) {
	for i, t := range r.Plain {
		if dir != "" && (filepath.Clean(dir) == filepath.Clean(t.Path) || gitx.Within(dir, t.Path)) {
			r.Plain = slices.Delete(r.Plain, i, i+1)
			return t, true
		}
	}
	return PlainTree{}, false
}

// ToolCall is a tool the session called whose result has not come back. A call ends with its result
// (PostToolUse, PostToolUseFailure, PermissionDenied); a call the human refused at its permission prompt
// ends with nothing, so every call of the main agent ends with its turn, and a subagent's with the
// subagent. A call older than ToolStale is not trusted to be running.
type ToolCall struct {
	ID    string    `json:"id,omitempty"`    // its tool_use_id
	Agent string    `json:"agent,omitempty"` // the subagent that made it ("" for the main agent)
	Tool  string    `json:"tool"`
	Since time.Time `json:"since"`
}

// ToolStale is how long a tool call is taken to be running at most: by then a command has long moved to
// the background (foreground_max), and a call still on record most likely ended with no hook.
const ToolStale = 10 * time.Minute

// maxTools bounds the calls on record: calls that end with no hook must not pile up.
const maxTools = 32

// ToolStarted records call c as running.
func (r *Runtime) ToolStarted(c ToolCall) {
	if c.ID != "" && slices.ContainsFunc(r.Tools, func(o ToolCall) bool { return o.ID == c.ID }) {
		return
	}
	r.Tools = append(r.Tools, c)
	if n := len(r.Tools) - maxTools; n > 0 {
		r.Tools = slices.Delete(r.Tools, 0, n)
	}
}

// ToolEnded records that a call of agent's ended: the call id, or, without one, agent's oldest call of
// tool.
func (r *Runtime) ToolEnded(id, agent, tool string) {
	i := slices.IndexFunc(r.Tools, func(c ToolCall) bool {
		if id != "" {
			return c.ID == id
		}
		return c.Agent == agent && c.Tool == tool
	})
	if i >= 0 {
		r.Tools = slices.Delete(r.Tools, i, i+1)
	}
}

// ToolsEnded records that every call of agent ended ("" for the main agent): its turn, or the subagent,
// is over.
func (r *Runtime) ToolsEnded(agent string) {
	r.Tools = slices.DeleteFunc(r.Tools, func(c ToolCall) bool { return c.Agent == agent })
}

// InFlight lists the tool calls running at now: those on record, short of ToolStale.
func (r Runtime) InFlight(now time.Time) []ToolCall {
	var out []ToolCall
	for _, c := range r.Tools {
		if now.Sub(c.Since) < ToolStale {
			out = append(out, c)
		}
	}
	return out
}
