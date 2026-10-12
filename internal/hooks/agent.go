package hooks

import (
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent (docs/research/visible-workers.md) is one of baton's phases at work as a background
// subagent of the lead's session, which baton's mod spawns and every one of whose hooks carries its
// agent_id. OpenRun routes those hooks to its phase's sub-run, so that every guarantee a worker's session
// had holds for it, by the same code: its brief, the Stop table, the refusals, the deliveries at its tool
// calls.

// spawning records the call baton's mod makes in the lead's session to start a phase agent (modCall), so
// that the worktree it asks for is its phase's (worktreeCreate). Its name is the phase's (the bridge's
// spawn command says so).
func (h *handlers) spawning(c Context) {
	ti, _ := c.Input["tool_input"].(map[string]any)
	if str(c.Input, "tool_name") != "Agent" || str(ti, "subagent_type") != state.PhaseAgent || str(ti, "name") == "" {
		return
	}
	sp := state.Spawning{Phase: str(ti, "name"), Prompt: str(c.Input, "prompt_id"), ToolUse: str(c.Input, "tool_use_id"), At: c.Now}
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.Spawning = &sp
		return nil
	})
	if err == nil {
		s.Event("agent_spawning", map[string]any{"phase": sp.Phase, "tool_use": sp.ToolUse})
	}
}

// spawned ends the call baton's mod made to start a phase agent, at its result: a worktree asked for after
// it is not the phase's.
func (h *handlers) spawned(c Context) (Result, error) {
	id := str(c.Input, "tool_use_id")
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if sp := st.Run.Spawning; sp != nil && sp.ToolUse == id {
			st.Run.Spawning = nil
		}
		return nil
	})
	return Result{}, ok(err)
}

// agentStart is SubagentStart for the phase agent of s, the sub-run OpenRun opened for it: a turn of its
// own begins. The first time this agent starts, it is briefed: a worker's brief, with its phase started,
// or, for an agent that takes up a phase another agent left unfinished, told so and to check what is done
// first. A message that starts it again, idle, finds it briefed: it keeps its conversation, and is given
// what baton has for it (its results, baton's messages), as the prompt's hook gives them to the lead: the
// message that woke it (loop.line) only says they come.
func (h *handlers) agentStart(c Context, s *state.Store) (Result, error) {
	origin := state.OriginOf(s.Root) // read before taking the lock: git can be slow
	brief, started := false, false
	var dl delivery
	var n news
	_, st, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.TurnOpen, st.Run.TurnStarted, st.Run.TurnBy = true, c.Now, "baton"
		if st.Mode == state.ModeIdle {
			return nil
		}
		if st.Run.Briefed == s.Agent {
			dl, n = take(st, c.Now), takeNews(st, s)
			return nil
		}
		st.Run.Briefed, brief = s.Agent, true
		if ps := st.Phases[st.Current]; st.Mode == state.ModeRunning && (ps == nil || ps.Status == state.PhasePending) {
			state.Start(st, c.Now, origin)
			started = true
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	if !brief {
		return resumed(s, dl, n), nil
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return Result{}, nil // a sub-run with no plan has nothing to brief
	}
	if started {
		s.Event("phase_started", map[string]any{"phase": st.Current, "agent": s.Agent})
	}
	text := h.workerBrief(c, s, pl, st, !started)
	return Result{Output: map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": "SubagentStart", "additionalContext": text},
	}}, nil
}

// resumed is what a phase agent started again by a message is given: its results and baton's messages.
func resumed(s *state.Store, dl delivery, n news) Result {
	if !dl.any() && !n.any() {
		return Result{}
	}
	text := strings.TrimSpace(n.text + "\n\n" + dl.text(s))
	if dl.any() {
		dl.event(s, "resume")
	}
	if n.any() {
		n.event(s, "resume")
	}
	return Result{Output: map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": "SubagentStart", "additionalContext": text},
	}}
}

// agentStop is SubagentStop for the phase agent of s: its stop is decided by the Stop table, for its sub-run,
// as a worker's session's stop was, and a refused stop runs it on with the reason. Nothing it asked on the
// lead's screen is open any more.
func (h *handlers) agentStop(c Context, s *state.Store) (Result, error) {
	res, err := h.stop(c)
	h.onScreen(c, s, func(st *state.State) { st.Run.Dialogs.CloseAgent(s.Agent) })
	return res, err
}
