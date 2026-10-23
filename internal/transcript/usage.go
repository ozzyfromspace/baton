package transcript

import (
	"encoding/json"
	"time"
)

// Limit is a turn that ended on one of the account's usage limits, as Claude Code wrote it in the stopped
// loop's own transcript, before its StopFailure hook ran (docs/research/usage.md): which window it was, and
// when that resets. StopFailure's own input says neither.
type Limit struct {
	Kind   string    // quotaLimits.rateLimitType: five_hour, seven_day, seven_day_opus, …; "" when not said
	Resets time.Time // quotaLimits.resetsAt; zero when not said
}

// quota is the part of an API error entry's quotaLimits baton reads.
type quota struct {
	RateLimitType string   `json:"rateLimitType"`
	ResetsAt      *float64 `json:"resetsAt"` // Unix seconds
}

// limitOf is the usage limit an assistant entry records: one Claude Code wrote for an API error of kind
// rate_limit (isApiErrorMessage, error "rate_limit"), with what its quotaLimits says. A 429 whose quota
// headers did not parse has none, so its Limit says nothing more. nil for any other entry.
func limitOf(l line) *Limit {
	var kind string
	if !l.IsAPIErrorMessage || json.Unmarshal(l.Error, &kind) != nil || kind != "rate_limit" {
		return nil
	}
	lim := &Limit{}
	var q quota
	if json.Unmarshal(l.QuotaLimits, &q) == nil {
		lim.Kind = q.RateLimitType
		if q.ResetsAt != nil && *q.ResetsAt > 0 {
			lim.Resets = time.Unix(int64(*q.ResetsAt), 0)
		}
	}
	return lim
}

// LastLimit is the usage limit that ended the last turn of the entries, oldest first: one with no prompt
// or reply of the model's after it. nil if the last turn did not end on one. agent: the entries are a
// subagent's own transcript's.
func LastLimit(entries []Entry, agent bool) *Limit {
	var lim *Limit
	for _, e := range entries {
		switch {
		case !own(e, agent):
		case e.Limit != nil:
			lim = e.Limit
		case e.Prompt(), e.Type == "assistant":
			lim = nil // a turn after it, or the model going on
		}
	}
	return lim
}

// Limited reads the end of the transcript at path for the usage limit that ended its last turn
// (LastLimit); agent: it is a subagent's own.
func Limited(path string, agent bool) (*Limit, error) {
	entries, err := Tail(path, TailBytes)
	if err != nil {
		return nil, err
	}
	return LastLimit(entries, agent), nil
}
