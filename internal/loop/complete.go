package loop

import (
	"context"
	"errors"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The host asks a model a question through baton's mod (bridge.Complete): the call goes through the lead
// session's own client and credentials, with no history and no tools, and nothing of it reaches the lead's
// conversation or starts a turn (spike 41). So it is made only where the mod runs, never in a serial run.
// The host writes every word of it, and the mod only makes the call. It is advice, never a step of the
// run: whatever stands in its way, the caller goes on without an answer.

// Completion is one model call baton's mod makes for the host.
type Completion struct {
	Model     string        // an alias (haiku, opus) or a full model id
	System    string        // its system prompt ("" for none)
	Prompt    string        // the one user message
	MaxTokens int           // the reply's token cap (0: Claude Code's default, 1024)
	Timeout   time.Duration // how long the call itself may take (0: CompleteTimeout)
}

// Answer is a model's answer to a Completion: its text (cut at bridge.AnswerCut characters), how long the
// call took, and what it used (nil when the mod did not say).
type Answer struct {
	Text  string
	Took  time.Duration
	Usage *bridge.Usage
}

// Why a Completion has no answer (Unanswered.Why). The model's side is said as the mod says it:
// "api-error" (its status and kind in Detail), "empty-reply", "aborted" (the call's own time limit), or
// "refused" (Claude Code would not send it); "failed" is an error the mod gave no reason for (a mod too old
// to know the command).
const (
	NoMod       = "no-mod"      // no mod baton works with has said hello in the lead's claude
	SerialRun   = "serial"      // the run is serial: the lead's claude has no mod baton works with
	Unissued    = "unissued"    // the command could not be written for the mod
	Interrupted = "interrupted" // the mod was reloaded while the call ran
	TimedOut    = "timeout"     // no ack by the deadline: the command was taken back
	Cancelled   = "cancelled"   // the caller gave up first: the command was taken back
)

// Unanswered is why a Completion has no answer: one of the reasons above, and what was said of it.
type Unanswered struct {
	Why    string
	Detail string
}

func (u *Unanswered) Error() string {
	if u.Detail == "" {
		return "no answer: " + u.Why
	}
	return "no answer: " + u.Why + ": " + u.Detail
}

// CompleteTimeout bounds a model call that names no time limit of its own.
const CompleteTimeout = time.Minute

// completeSlack is how much longer than the call's own time limit the host waits for its ack when the
// caller gives no deadline: the mod's poll (half a second), and a report's two lock waits (5 s each).
const completeSlack = 15 * time.Second

// completePoll is how often the host looks for the mod's ack (tests shorten it).
var completePoll = 250 * time.Millisecond

// Complete has baton's mod in the lead's claude make the model call c (bridge.Complete), and waits for its
// ack until ctx is done (with no deadline of ctx's, c's time limit and completeSlack): the answer, or an
// *Unanswered saying why there is none. s is the run's store, the lead's, whose bridge the mod polls; a
// phase agent's sub-run has no mod of its own. A command still open at the deadline is taken back
// (bridge.Void), so the mod never makes the call, and an ack after that is kept for no one. It holds no
// lock while it waits, and runs no faster than a model, so callers run it off the host's tick.
func Complete(ctx context.Context, s *state.Store, c Completion) (Answer, error) {
	if s.Worker() != "" {
		return Answer{}, &Unanswered{Why: NoMod, Detail: "a phase agent's sub-run has no mod of its own"}
	}
	limit := c.Timeout
	if limit <= 0 {
		limit = CompleteTimeout
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit+completeSlack)
		defer cancel()
	}
	b := bridge.Open(s.Dir, s.Now, func(kind string, fields map[string]any) { s.Event(kind, fields) })
	if u := modFor(s, b); u != nil {
		return Answer{}, u
	}
	cmd, err := b.Issue(bridge.Command{Kind: bridge.Complete, Instance: s.Instance, Model: c.Model, System: c.System,
		Prompt: c.Prompt, MaxTokens: c.MaxTokens, TimeoutMs: limit.Milliseconds()})
	if err != nil {
		return Answer{}, &Unanswered{Why: Unissued, Detail: err.Error()}
	}
	tick := time.NewTicker(completePoll)
	defer tick.Stop()
	for {
		if rep, ok, _ := b.Answer(s.Instance, cmd.ID); ok {
			return answerOf(rep)
		}
		select {
		case <-tick.C:
			continue
		case <-ctx.Done():
		}
		// One more look, then the command is taken back. A command no longer open was acked meanwhile.
		if rep, ok, _ := b.Answer(s.Instance, cmd.ID); ok {
			return answerOf(rep)
		}
		if found, err := b.Void(s.Instance, cmd.ID); err == nil && !found {
			if rep, ok, _ := b.Answer(s.Instance, cmd.ID); ok {
				return answerOf(rep)
			}
		}
		why := TimedOut
		if errors.Is(ctx.Err(), context.Canceled) {
			why = Cancelled
		}
		return Answer{}, &Unanswered{Why: why, Detail: ctx.Err().Error()}
	}
}

// modFor says why the lead's claude has no mod to make a model call (nil if it has one): a run gone
// serial, by the mod's own hello in this claude (one baton does not work with) or by the host's decision
// (state.Run.Serial), or no hello from this claude yet.
func modFor(s *state.Store, b *bridge.Bridge) *Unanswered {
	rec, err := b.Peek()
	if err != nil {
		return &Unanswered{Why: NoMod, Detail: err.Error()}
	}
	h := rec.Hello
	switch {
	case h.Ready(s.Instance):
		return nil
	case h != nil && h.Instance == s.Instance && h.Refused != "":
		return &Unanswered{Why: SerialRun, Detail: h.Refused}
	}
	if st, err := s.Load(); err == nil && st.Run.Serial != nil {
		return &Unanswered{Why: SerialRun, Detail: st.Run.Serial.Why}
	}
	return &Unanswered{Why: NoMod, Detail: "baton's mod has not said hello in the lead's claude"}
}

// answerOf is what the mod's ack rep of a complete command says: the answer, or why there is none.
func answerOf(rep bridge.Report) (Answer, error) {
	switch {
	case rep.Interrupted:
		return Answer{}, &Unanswered{Why: Interrupted, Detail: "the mod was reloaded while the call ran"}
	case !rep.OK:
		why := rep.Reason
		if why == "" {
			why = "failed"
		}
		return Answer{}, &Unanswered{Why: why, Detail: rep.Error}
	}
	return Answer{Text: rep.Result, Took: time.Duration(rep.Ms) * time.Millisecond, Usage: rep.Usage}, nil
}
