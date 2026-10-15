package state

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// A loop that stopped by no one's choice is brought back by baton, for as long as it takes: the lead after
// its claude exited by itself mid-plan (cli/recover.go), and the lead or a phase agent after an API error a
// fresh session avoids (StopError.Afresh). The first MaxRecoveries in RecoveryWindow come at once; past
// them, each comes after a wait (Backoff), so that a loop that keeps failing costs little, and nothing
// gives up on the run.
const (
	MaxRecoveries  = 2
	RecoveryWindow = 30 * time.Minute
)

// Recent is the recoveries of rs within RecoveryWindow of now: older ones are forgotten.
func Recent(rs []Recovery, now time.Time) []Recovery {
	return slices.DeleteFunc(slices.Clone(rs), func(r Recovery) bool { return now.Sub(r.At) >= RecoveryWindow })
}

// Backoff is how long a loop whose recoveries are rs waits, at now, before it is brought back once more:
// nothing while fewer than MaxRecoveries came in RecoveryWindow; past that first, and every once one of
// them has waited. A loop that stayed up for RecoveryWindow starts over.
func Backoff(rs []Recovery, now time.Time, first, every time.Duration) time.Duration {
	recent := Recent(rs, now)
	switch {
	case len(recent) < MaxRecoveries:
		return 0
	case slices.ContainsFunc(recent, func(r Recovery) bool { return r.Waited > 0 }):
		return every
	}
	return first
}

// conversationErrors are what Claude Code says, in an invalid_request's details or its message, when the
// API refused the conversation itself: too long for the model's context, a request too large, a tool
// history it cannot take, an image or a document it rejects, or a context that refills as fast as it is
// compacted (Claude Code 2.1.300).
var conversationErrors = regexp.MustCompile(`prompt is too long|input is too long|request too large|request_too_large|` +
	`request_body_over_limit|exceeds? (the )?context|context (window|limit)|autocompact is thrashing|` +
	`orphaned tool_result|duplicate tool_use|tool use concurrency|tool_use_id|tool_use ids were found without|tool_result block|` +
	`image was too large|image in the conversation exceeds|pdf too large|pdf file was not valid|pdf is password protected`)

// Afresh reports an API error a fresh session from the brief avoids: the API refused the conversation itself
// (conversationErrors), so every turn of it would fail the same way, and one that starts from the brief
// would not. Not a refusal, which a fresh session may meet again (docs/research/recovery.md), nor what the
// account, the model or the session's settings stand in the way of (a login, billing, a model it may not
// use): those stay the human's.
func (e StopError) Afresh() bool {
	text := strings.ToLower(strings.ReplaceAll(e.Details+"\n"+e.Message, "`", ""))
	return e.Error == "invalid_request" && conversationErrors.MatchString(text)
}

// RefusedNow reports a last turn of the run's own (the lead's, or a phase agent's in its sub-run) that the
// model's safeguards declined, and that nothing has gone on from since: baton types nothing into that
// conversation, and starts no fresh one; how it goes on is the human's.
func (st State) RefusedNow() bool {
	if e := st.Run.LastError; e != nil && e.Error == ErrRefusal && !st.Run.TurnStarted.After(e.At) {
		return true
	}
	return st.Run.Escalation != nil && st.Run.Escalation.Kind == "refused"
}
