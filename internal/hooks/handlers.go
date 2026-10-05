package hooks

import (
	"fmt"
	"math"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// NudgePrefix starts every message baton types or sends into the session, so the hooks can tell
// baton's prompts from the human's.
const NudgePrefix = "[baton]"

// Deps gives handlers access to the project's state.
type Deps struct {
	// Open returns the store for the session's .baton directory, given the hook's environment.
	Open func(env func(string) string) (*state.Store, error)
}

// Handlers returns baton's handler for each hook event.
func Handlers(d Deps) map[string]Handler {
	h := &handlers{d}
	return map[string]Handler{
		"SessionStart":       h.sessionStart,
		"SessionEnd":         h.sessionEnd,
		"UserPromptSubmit":   h.userPromptSubmit,
		"PreToolUse":         h.preToolUse,
		"PostToolUse":        h.toolDone,
		"PostToolUseFailure": h.toolDone,
		"PermissionRequest":  h.permissionRequest,
		"PermissionDenied":   h.toolDone,
		"Notification":       h.notification,
		"SubagentStart":      h.subagentStart,
		"SubagentStop":       h.subagentStop,
		"Stop":               h.stop,
		"StopFailure":        h.stopFailure,
		"PreCompact":         h.preCompact,
		"PostCompact":        h.postCompact,
		"PostCompactRewake":  h.postCompactRewake,
		"SessionStartRewake": h.sessionStartRewake,
	}
}

type handlers struct{ d Deps }

// update applies fn to the state if this session owns the project; a non-owner session is dormant.
func (h *handlers) update(c Context, fn func(st *state.State, s *state.Store) error) (*state.Store, state.State, error) {
	s, err := h.d.Open(c.Env)
	if err != nil {
		return nil, state.State{}, err
	}
	instance := c.Env("BATON_INSTANCE")
	var owner bool
	st, err := s.Update(func(st *state.State) error {
		if owner = st.IsOwner(instance, c.Now); !owner {
			return errNotOwner
		}
		st.Run.LastActivity = c.Now
		if sid := str(c.Input, "session_id"); sid != "" {
			st.Run.SessionID = sid
		}
		if pm := str(c.Input, "permission_mode"); pm != "" {
			st.Run.PermissionMode = pm
		}
		return fn(st, s)
	})
	if err == errNotOwner {
		return nil, st, errNotOwner
	}
	return s, st, err
}

var errNotOwner = fmt.Errorf("this session does not own the project")

func ok(err error) error {
	if err == errNotOwner {
		return nil
	}
	return err
}

// preToolUse holds the model at a compaction it owes. After `baton done` or `baton checkpoint`, the turn
// must end so the host can compact; a model that carries on would start the next phase (or keep going)
// in the old context. So until then every main-agent tool call except baton's own CLI is denied, with
// the reason. Subagents are not held: they cannot end the main turn.
func (h *handlers) preToolUse(c Context) (Result, error) {
	var owed string
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Mode != state.ModeRunning || str(c.Input, "agent_id") != "" || isBatonCommand(c.Input) {
			return nil
		}
		switch {
		case st.BoundaryOwed:
			owed = "the phase is done and baton must compact the context before " + st.Current + " begins"
		case st.CheckpointOwed:
			owed = "you asked for a checkpoint and baton must compact the context first"
		}
		return nil
	})
	if err != nil || owed == "" {
		return Result{}, ok(err)
	}
	return Result{Output: map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": NudgePrefix + " End your turn now: " + owed + ". Do not start further work in this turn.",
		},
	}}, nil
}

// isBatonCommand reports a Bash call that runs baton's own CLI (always allowed).
func isBatonCommand(in map[string]any) bool {
	if str(in, "tool_name") != "Bash" {
		return false
	}
	ti, _ := in["tool_input"].(map[string]any)
	cmd := strings.TrimSpace(str(ti, "command"))
	return cmd == "baton" || strings.HasPrefix(cmd, "baton ")
}

func (h *handlers) activity(c Context) (Result, error) {
	_, _, err := h.update(c, func(*state.State, *state.Store) error { return nil })
	return Result{}, ok(err)
}

func (h *handlers) sessionStart(c Context) (Result, error) {
	source := str(c.Input, "source")
	var pl plan.Plan
	var havePlan bool
	s, st, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.Ended = nil
		if source != "compact" {
			st.Run.TurnOpen, st.Run.Dialog, st.Run.Subagents = false, nil, 0
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	s.Event("session_start", map[string]any{"source": source})
	if p, err := s.LoadPlan(); err == nil && st.Mode != state.ModeIdle {
		pl, havePlan = p, true
	}
	if source == "compact" {
		return h.afterCompaction(c, s)
	}
	if !havePlan {
		return Result{Output: map[string]any{"systemMessage": "baton: hosting this session (no plan attached — /baton plan or /baton attach)"}}, nil
	}
	return Result{Output: map[string]any{
		"systemMessage": "baton: hosting · " + progressLine(pl, st),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": Primer(pl, st),
		},
	}}, nil
}

// sessionStartRewake runs in the background when a session resumes. After an elevation it wakes the model
// with what it was about to do (BATON_PENDING, set by the relaunch), exactly once.
func (h *handlers) sessionStartRewake(c Context) (Result, error) {
	pending := strings.TrimSpace(c.Env("BATON_PENDING"))
	if pending == "" || str(c.Input, "source") != "resume" {
		return Result{}, nil
	}
	deliver := false
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.PendingDone != pending {
			st.Run.PendingDone, deliver = pending, true
		}
		return nil
	})
	if err != nil || !deliver {
		return Result{}, ok(err)
	}
	s.Event("pending_delivered", map[string]any{"pending": pending})
	return Result{Rewake: NudgePrefix + " This session is now hosted by baton (same conversation). Continue with: " + pending}, nil
}

// Primer tells the model, at the start of a hosted session, how baton expects it to report progress.
func Primer(pl plan.Plan, st state.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This session is hosted by baton, which runs the attached plan %q phase by phase and compacts the context between phases. ", pl.Title)
	switch st.Mode {
	case state.ModeComplete:
		b.WriteString("Every phase of the plan is done.")
		return b.String()
	case state.ModePaused:
		b.WriteString("baton is paused: the human is driving; do not report phase progress unless asked. ")
	}
	if i := pl.Index(st.Current); i >= 0 {
		fmt.Fprintf(&b, "Current phase: %s — %s (see %s). ", st.Current, pl.Phases[i].Title, pl.File)
	}
	b.WriteString("Report progress only with these commands (Bash tool): " +
		"when the current phase is complete and committed, run `baton done <phase> --notes \"<what later phases need to know>\"` and end your turn — baton then compacts the context and starts the next phase with a fresh brief. " +
		"If you cannot continue without the human, run `baton blocked \"<why>\"` and end your turn. " +
		"If you must wait for something outside you (a build, a deploy), run `baton waiting \"<what>\" --until <duration>`. " +
		"In a long phase, at a safe point (work committed), `baton checkpoint --notes \"<where you are>\"` compacts mid-phase. " +
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why. " +
		"(If the human wants to talk instead of running the plan, they can run /baton pause.)")
	return b.String()
}

func progressLine(pl plan.Plan, st state.State) string {
	done := 0
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == state.PhaseDone {
			done++
		}
	}
	line := fmt.Sprintf("%s · %d/%d phases done", pl.Title, done, len(pl.Phases))
	if st.Current != "" {
		line += " · current " + st.Current
	}
	if st.Mode != state.ModeRunning {
		line += " · " + st.Mode
	}
	return line
}

func (h *handlers) sessionEnd(c Context) (Result, error) {
	reason := str(c.Input, "reason")
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.Ended = &state.Ended{Reason: reason, At: c.Now}
		st.Run.TurnOpen = false
		if st.Mode == state.ModeRunning && !deliberateEnd[reason] {
			notice(st, "session_ended", "the session ended mid-plan ("+reason+")", c.Now)
		}
		return nil
	})
	if err == nil {
		s.Event("session_end", map[string]any{"reason": reason})
	}
	return Result{}, ok(err)
}

// deliberateEnd are session-end reasons that mean the human chose to end the session.
var deliberateEnd = map[string]bool{"prompt_input_exit": true, "clear": true, "logout": true}

// TurnSource classifies a submitted prompt: a rewake or nudge from baton, another task notification,
// or the human.
func TurnSource(prompt string) string {
	p := strings.TrimSpace(prompt)
	switch {
	case strings.HasPrefix(p, NudgePrefix):
		return "baton"
	case strings.HasPrefix(p, "<task-notification>"):
		if strings.Contains(p, NudgePrefix) {
			return "baton"
		}
		return "system"
	default:
		return "human"
	}
}

func (h *handlers) userPromptSubmit(c Context) (Result, error) {
	by := TurnSource(str(c.Input, "prompt"))
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.TurnOpen, st.Run.TurnBy, st.Run.TurnStarted = true, by, c.Now
		st.Run.Dialog = nil
		if by == "human" {
			// The human is engaged: whatever baton escalated, they have it now.
			st.Run.StopBlocks, st.Run.Escalation = 0, nil
		}
		return nil
	})
	if err == nil {
		s.Event("turn_started", map[string]any{"by": by})
	}
	return Result{}, ok(err)
}

func (h *handlers) permissionRequest(c Context) (Result, error) {
	tool := str(c.Input, "tool_name")
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.Dialog = &state.Dialog{Tool: tool, Since: c.Now}
		return nil
	})
	if err == nil {
		s.Event("dialog_open", map[string]any{"tool": tool})
	}
	return Result{}, ok(err)
}

func (h *handlers) toolDone(c Context) (Result, error) {
	tool := str(c.Input, "tool_name")
	var v valveAction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.Dialog != nil && st.Run.Dialog.Tool == tool {
			st.Run.Dialog = nil
		}
		if c.Event == "PostToolUse" && str(c.Input, "agent_id") == "" {
			v = contextValves(st, valve.FromEnv(c.Env))
		}
		return nil
	})
	if err != nil || v.event == "" {
		return Result{}, ok(err)
	}
	s.Event(v.event, v.fields)
	return Result{Output: map[string]any{
		"systemMessage":      v.say,
		"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse", "additionalContext": v.tell},
	}}, nil
}

// valveAction is what a context valve does: an event to log, a line for the human, words for the model.
type valveAction struct {
	event     string
	fields    map[string]any
	say, tell string
}

// contextValves runs the two mid-phase valves after a main-agent tool call. Past the checkpoint line,
// the model is asked to checkpoint at its next safe point. Past the warning line, the model is asked to
// put a fixed question to the human: checkpoint now, or keep going and let Claude Code compact on its
// own. Each fires once, and re-arms only after the context falls a tenth of the window below its line,
// so a compaction that leaves the context high cannot start a loop. Subagents are never asked: they
// cannot end the main session's turn.
func contextValves(st *state.State, vs valve.Settings) valveAction {
	cu := st.Run.Context
	if cu == nil {
		return valveAction{}
	}
	lim, used := vs.Limits(cu.WindowSize), cu.Used()
	if lim.Window == 0 {
		return valveAction{}
	}
	rearm := lim.Window / 10
	if st.Run.ContextNudged && used < lim.Checkpoint-rearm {
		st.Run.ContextNudged = false
	}
	if st.Run.ContextWarned && used < lim.Warn-rearm {
		st.Run.ContextWarned = false
	}
	if st.Mode != state.ModeRunning || st.CheckpointOwed || st.BoundaryOwed || st.Run.Compaction.InFlight() {
		return valveAction{}
	}
	pct := 100 * float64(used) / float64(lim.Window)
	switch {
	case lim.Warn > 0 && used >= lim.Warn && !st.Run.ContextWarned:
		// The warning includes the checkpoint option, so the nudge has nothing left to say.
		st.Run.ContextWarned, st.Run.ContextNudged = true, true
		return valveAction{
			event:  "context_warning",
			fields: map[string]any{"tokens": used, "warn": lim.Warn, "limit": lim.Window, "auto_compact": lim.AutoAt},
			say:    fmt.Sprintf("baton: context at %s of %s → asking you whether to checkpoint", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell:   warnText(used, lim),
		}
	case lim.Checkpoint > 0 && used >= lim.Checkpoint && !st.Run.ContextNudged:
		st.Run.ContextNudged = true
		return valveAction{
			event:  "context_nudge",
			fields: map[string]any{"pct": math.Round(pct), "tokens": used, "checkpoint": lim.Checkpoint, "limit": lim.Window},
			say:    fmt.Sprintf("baton: context at %s of %s → checkpoint at the next safe point", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell: fmt.Sprintf("%s Checkpoint due: the context holds %s tokens, %.0f%% of its %s limit. Finish the step you are on and commit, then run "+
				"`baton checkpoint --notes \"<where you are and what is left>\"` and end your turn: baton compacts the context and you continue this phase from your notes. "+
				"(If the phase is already complete, run `baton done` instead.)", NudgePrefix, valve.Tokens(used), pct, valve.Tokens(lim.Window)),
		}
	}
	return valveAction{}
}

// warnText makes the model put a fixed question to the human. AskUserQuestion reaches every device the
// human uses (and the watchdog pushes a notification if it goes unanswered).
func warnText(used int, lim valve.Limits) string {
	q := fmt.Sprintf("baton: the context holds %s tokens, past the %s warning line. Claude Code compacts it on its own at about %s. Checkpoint now?",
		valve.Tokens(used), valve.Tokens(lim.Warn), valve.Tokens(lim.AutoAt))
	return fmt.Sprintf("%s Context warning. Before anything else, call the AskUserQuestion tool with the question %q and two options: "+
		"\"Checkpoint now\" (description: \"Finish the current step, save notes and compact; this phase continues from the notes\") and "+
		"\"Keep going\" (description: \"Carry on; Claude Code compacts on its own when the context is full\"). "+
		"If they choose Checkpoint now, finish the step you are on and commit, then run `baton checkpoint --notes \"<where you are and what is left>\"` and end your turn. "+
		"If they choose Keep going, carry on with the phase.", NudgePrefix, q)
}

func (h *handlers) notification(c Context) (Result, error) {
	kind := str(c.Input, "notification_type")
	s, _, err := h.update(c, func(*state.State, *state.Store) error { return nil })
	if err == nil {
		s.Event("notification", map[string]any{"type": kind})
	}
	return Result{}, ok(err)
}

func (h *handlers) subagentStart(c Context) (Result, error) {
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error { st.Run.Subagents++; return nil })
	return Result{}, ok(err)
}

func (h *handlers) subagentStop(c Context) (Result, error) {
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.Subagents > 0 {
			st.Run.Subagents--
		}
		return nil
	})
	return Result{}, ok(err)
}

// recordStop notes what every Stop tells us; the decision about the stop is made in stop.go.
func recordStop(st *state.State, c Context) {
	st.Run.TurnOpen, st.Run.LastStop, st.Run.Dialog = false, c.Now, nil
	st.Run.Background = tasks(c.Input["background_tasks"])
	if crons, okc := c.Input["session_crons"].([]any); okc {
		st.Run.Crons = len(crons)
	} else {
		st.Run.Crons = 0
	}
}

func (h *handlers) stopFailure(c Context) (Result, error) {
	e := state.StopError{Error: str(c.Input, "error"), Details: str(c.Input, "error_details"), At: c.Now}
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.TurnOpen, st.Run.LastError = false, &e
		return nil
	})
	if err == nil {
		s.Event("stop_failure", map[string]any{"error": e.Error})
	}
	return Result{}, ok(err)
}

func (h *handlers) preCompact(c Context) (Result, error) {
	trigger := str(c.Input, "trigger")
	var cp state.Compaction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		comp := &st.Run.Compaction
		requested := comp.Status == state.CompactQueued || comp.Status == state.CompactTyped
		switch {
		case requested && trigger == "manual":
			comp.ByBaton = true
		case st.BoundaryOwed || st.CheckpointOwed:
			// Any compaction while one is owed (an auto-compaction, or the human typing /compact)
			// satisfies it, so baton never compacts twice in a row.
			comp.ByBaton = true
			if comp.Reason == "" {
				comp.Reason = reasonOwed(st)
			}
			if comp.Status == "" || comp.Status == state.CompactDone || comp.Status == state.CompactFailed {
				comp.Epoch++
			}
		default:
			comp.ByBaton = false
		}
		if comp.ByBaton {
			comp.Status = state.CompactActive
		}
		comp.Trigger, comp.Started = trigger, c.Now
		cp = *comp
		return nil
	})
	if err == nil {
		s.Event("compact_started", map[string]any{"trigger": trigger, "by_baton": cp.ByBaton, "epoch": cp.Epoch, "reason": cp.Reason})
	}
	return Result{}, ok(err)
}

func reasonOwed(st *state.State) string {
	if st.BoundaryOwed {
		return "boundary"
	}
	return "checkpoint"
}

func (h *handlers) postCompact(c Context) (Result, error) {
	var cp state.Compaction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		comp := &st.Run.Compaction
		if comp.ByBaton && comp.Status == state.CompactActive {
			comp.Status = state.CompactDone
		}
		comp.Finished = c.Now
		cp = *comp
		return nil
	})
	if err == nil {
		s.Event("compact_finished", map[string]any{"trigger": cp.Trigger, "by_baton": cp.ByBaton, "epoch": cp.Epoch})
	}
	return Result{}, ok(err)
}

func tasks(v any) []state.Task {
	list, _ := v.([]any)
	var out []state.Task
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, state.Task{ID: str(m, "id"), Type: str(m, "type"), Status: str(m, "status"), Description: str(m, "description")})
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
