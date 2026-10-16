package hooks

import (
	"regexp"
	"strings"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase's id is the human's handle on baton's agent on it: `@P23 <words>` at the lead's prompt writes
// to that agent. Claude Code routes `@name` at the main prompt only to agent-team teammates; for a
// subagent the whole line is an ordinary prompt of the lead's (spike 40). So the lead's UserPromptSubmit
// routes it: by its shape alone, as the hook cannot tell who typed it, and only for a prompt TurnSource
// takes for the human's (baton's lines start "[baton]", agents' reports with their own frames).

// mentionRe is a prompt that starts with `@<name>`, a name an agent may have (plan's names.go), then
// whitespace, then the words.
var mentionRe = regexp.MustCompile(`^@([A-Za-z0-9][A-Za-z0-9_-]*)\s+(\S[\s\S]*)$`)

// mention reads prompt for the human's words to an agent: the name it starts with, and the words after it.
func mention(prompt string) (name, words string, ok bool) {
	m := mentionRe.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// toLead is what the lead's model and the human are told of a prompt that starts with a phase's handle,
// whose phase no agent of baton's works on: the prompt is the lead's.
type toLead struct{ context, say string }

// addressed routes the human's prompt c that starts with one of the run's phases' handles (mention;
// plan.Plan.Named, ignoring case). For a phase whose agent is at work (state.Addressee), the words are
// queued for the host to send it (state.QueueWords, loop/words.go), and the prompt is blocked: the lead's
// model never sees it, no turn starts, and Claude Code shows the human the reason, which says what baton
// did (spike 40). It then returns that answer, routed. For a phase no agent works on, nothing is woken: it
// returns what the lead's model and the human are told, and the prompt goes on to the lead's model.
// Anything else (no handle, no plan, an id none of the plan's, a run another session drives, a run or a
// plan it cannot read) is an ordinary prompt: nil.
func (h *handlers) addressed(c Context) (res Result, routed bool, lead *toLead) {
	prompt := str(c.Input, "prompt")
	name, words, ok := mention(prompt)
	if !ok {
		return Result{}, false, nil
	}
	s, err := h.d.Open(c)
	if err != nil || s == nil || s.Worker() != "" {
		return Result{}, false, nil
	}
	if peek, err := s.Peek(); err != nil || peek.Mode == state.ModeIdle {
		return Result{}, false, nil
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return Result{}, false, nil
	}
	phase := pl.Named(name)
	if phase == "" {
		return Result{}, false, nil
	}
	known := map[string]bool{}
	if agents, err := bridge.Open(s.Dir, s.Now, nil).Agents(); err == nil {
		for _, a := range agents {
			known[a.ID] = true
		}
	}
	var to, agent string
	var queued bool
	if _, err := s.Update(func(st *state.State) error {
		if !st.IsOwner(s.Instance, c.Now) {
			return errNotOwner
		}
		to, agent = state.Addressee(*st, phase, func(id string) bool { return known[id] })
		if to == state.ToAgent {
			queued = state.QueueWords(st, state.HumanWords{Phase: phase, Agent: agent, Text: words, Prompt: prompt, At: c.Now})
		}
		return nil
	}); err != nil {
		return Result{}, false, nil
	}
	if to == state.ToAgent {
		fields := map[string]any{"phase": phase, "agent": agent, "routed": "agent", "step": "queued", "chars": len([]rune(words))}
		if !queued {
			fields["again"] = true // the same words, still queued: sent once
		}
		s.Event("human_to_agent", fields)
		return Result{Output: map[string]any{"decision": "block", "reason": decide.SentToAgent(phase)}}, true, nil
	}
	why := decide.NoAgent(phase, to)
	s.Event("human_to_agent", map[string]any{"phase": phase, "routed": "lead", "stands": to, "why": why})
	return Result{}, false, &toLead{context: decide.WordsForLead(phase, why), say: decide.WordsToLead(phase, why)}
}

// letGo answers a hook of phase agent agent, whose sub-run is s, if baton let go of it for good
// (state.Project.LetGo: its worker retired, or a fresh agent put in its place), and reports whether it did.
// The human woke it from its own view in Claude Code's agent panel, which nothing of baton's sees, and it
// may be working in the lead's own checkout, or beside the agent that took its place (spike 40): every
// tool call it makes is refused with why, and every other hook of it, its stop among them, is let through
// with nothing recorded, so it ends its turn having changed nothing, and nothing waits on it. Its waking is
// logged once (retired_agent_woke), in the run its phase was of. A record it cannot read lets it be.
func (h *handlers) letGo(c Context, s *state.Store, agent string) (Result, bool) {
	p := s.ProjectOf()
	if p.LetGoOf(agent) == "" {
		return Result{}, false
	}
	phase, why := s.Worker(), ""
	up, err := s.ParentRun()
	if err == nil {
		if st, err := up.Peek(); err == nil {
			to, _ := state.Addressee(st, phase, func(string) bool { return true })
			why = decide.LetGoWhy(phase, to)
		}
	}
	if why == "" {
		why = decide.LetGoWhy(phase, "")
	}
	if p.Woke(agent) && up != nil {
		fields := map[string]any{"agent": agent, "phase": phase, "hook": c.Event}
		if tool := str(c.Input, "tool_name"); tool != "" {
			fields["tool"] = tool
		}
		up.Event("retired_agent_woke", fields)
	}
	if c.Event == "PreToolUse" {
		return permission("deny", decide.LetGoRefused(phase, why)), true
	}
	return Result{}, true
}
