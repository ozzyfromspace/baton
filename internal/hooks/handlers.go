package hooks

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
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
		"PreToolUse":         h.activity,
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
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why.")
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
		return nil
	})
	if err == nil {
		s.Event("session_end", map[string]any{"reason": reason})
	}
	return Result{}, ok(err)
}

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
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.Dialog != nil && st.Run.Dialog.Tool == tool {
			st.Run.Dialog = nil
		}
		return nil
	})
	return Result{}, ok(err)
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
