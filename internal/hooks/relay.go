package hooks

import (
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton's mod puts the run's phase agents' questions to the human (state.Relay), one at a time, in Claude
// Code's own dialog, on the host's say-so (loop.Coordinator.relay): no model is asked to ask them. The
// question's dialog opening is the question asked (relayAsked); the mod reports the answer, or that it
// settled the question at its deadline, and the host applies it to the agent's sub-run. A phase agent's
// own hooks only queue its questions (stop.go).

// modAsk reports a question a mod puts to the human in the lead's session (a call of the mod's own, never
// the model's). baton's is a question baton issued (matchIssued); its result reaches no model.
func modAsk(in map[string]any) bool {
	return str(in, "tool_name") == "AskUserQuestion" && str(in, "agent_id") == "" && strings.HasPrefix(str(in, "tool_use_id"), "toolu_plugin_")
}

// relayQuestion is phase agent question r as baton issued it.
func relayQuestion(r state.Relay) decide.Question {
	return decide.Question{Text: r.Question, Options: decide.RelayOptions(r)}
}

// relayFields are the fields of an event about phase agent question r.
func relayFields(r state.Relay, more map[string]any) map[string]any {
	fields := map[string]any{"phase": r.Phase, "type": r.Kind}
	if r.Decision != "" {
		fields["decision"] = r.Decision
	}
	for k, v := range more {
		fields[k] = v
	}
	return fields
}

// relayAsked records that the phase agent's question the mod was told to ask reached Claude Code's dialog
// queue, and pushes the human about a proposal (its question names the deadline). An agent's block or
// review pushed them already, as it began. A block's event says whether its question said nothing else in
// the run can move (run_waits).
func relayAsked(st *state.State, now time.Time) (event, bool) {
	r := st.RelayIssued()
	if r == nil || r.Asked {
		return event{}, false
	}
	r.Asked, r.AskedAt = true, now
	if r.Kind == state.RelayProposal {
		notice(st, "proposal", strings.TrimPrefix(r.Question, "baton: "), now)
	}
	fields := map[string]any{"deadline": r.Deadline.UTC().Format(time.RFC3339)}
	if r.Kind == state.RelayBlocked {
		fields["run_waits"] = r.RunWaits
	}
	return event{"relay_asked", relayFields(*r, fields)}, true
}
