package transcript

import "time"

// Served is the model and effort a request of a conversation was served, as Claude Code wrote them on its
// reply's entry (docs/research/tiers.md): what baton checks a phase's tier against, whatever it asked
// for, since Claude Code clamps an effort to the settings', the organization's and the model's caps with
// no word to anyone.
type Served struct {
	Model     string    // message.model: the model that answered, claude-opus-5-5
	Requested string    // requestedModel: the model asked for, which a fallback could differ from
	Effort    string    // effort: low … max; "" for a model that takes none
	At        time.Time // when the reply was written
}

// served is what an assistant entry's line says it was served, or nil: Claude Code's own synthetic replies
// (an API error's, a refusal's) name no model that answered.
func served(l line, model string, at time.Time) *Served {
	if l.Type != "assistant" || model == "" || model == "<synthetic>" || l.IsAPIErrorMessage {
		return nil
	}
	return &Served{Model: model, Requested: l.RequestedModel, Effort: l.Effort, At: at}
}

// LastServed reads the end of the transcript at path for the last reply of its own conversation that names
// what it was served, or nil when the end holds none; agent: the transcript is a subagent's own. It reads
// as far back as Context does, since a tool's result can be large.
func LastServed(path string, agent bool) (*Served, error) {
	for _, n := range []int64{TailBytes, contextBytes} {
		entries, err := Tail(path, n)
		if err != nil {
			return nil, err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; own(e, agent) && e.Served != nil {
				return e.Served, nil
			}
		}
	}
	return nil, nil
}
