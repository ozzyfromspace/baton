package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// limitGrace is how long after the account's usage limit resets the run stays held. Nothing resumes a
// stopped session by itself (docs/research/usage.md): once the hold ends, each loop's own retry
// (watchError) does.
const limitGrace = time.Minute

// usage notices a session of the run whose last turn ended on one of the account's usage limits. Every
// session of the run shares the account, so until the limit resets no worker starts, and nothing is typed
// into any of them (gateUsage). Which window it was, and when that resets, is the stop's own word from its
// transcript, else a status line reading at the limit's line (state.LimitWindow); when neither says,
// baton looks again after Timing.RateLimitRetry, as for one session (watchError). A limit hit is recorded
// once (state.RecordLimit), and the workers' Loops read it from here. A session that got a reply since the
// hit lifts the hold (state.LiftLimit). The coordinator's duties call it, so a run with no workers holds
// too.
func (c *Coordinator) usage(now time.Time, s *state.Store, st state.State) {
	by, at, stop := "lead", st.Run.LimitHit(), st.Run.LastError
	type session struct {
		who string
		run state.Runtime
	}
	runs := []session{{"lead", st.Run}}
	for _, w := range c.launched() {
		if sub, err := w.store.Load(); err == nil {
			runs = append(runs, session{w.phase, sub.Run})
			if hit := sub.Run.LimitHit(); hit.After(at) {
				by, at, stop = w.phase, hit, sub.Run.LastError
			}
		}
	}
	if !at.IsZero() && (st.Usage == nil || at.After(st.Usage.Hit)) {
		window, resets := state.LimitWindow(stop, st.Usage, at)
		until := at.Add(c.Lead.Timing.RateLimitRetry)
		if !resets.IsZero() {
			until = resets.Add(limitGrace)
		}
		hit := false
		if next, err := s.Update(func(st *state.State) error {
			hit = state.RecordLimit(st, by, at, until, window, resets)
			return nil
		}); err == nil {
			st = next
		}
		if hit {
			c.limitHit(now, s, st, by)
		}
	}
	if u := st.Usage; u != nil && now.Before(u.Limited) {
		for _, r := range runs {
			if !r.run.RepliedSince(u.Hit) {
				continue
			}
			lifted := false
			if next, err := s.Update(func(st *state.State) error { lifted = state.LiftLimit(st, now); return nil }); err == nil {
				st = next
			}
			if lifted {
				s.Event("usage_lifted", map[string]any{"by": r.who, "hit": u.Hit.UTC().Format(time.RFC3339), "window": u.Window})
				c.announce(s, decide.UsageLifted(whoHit(r.who)))
			}
			break
		}
	}
	var until int64
	if u := st.Usage; u != nil && !u.Limited.IsZero() {
		until = u.Limited.UnixNano()
	}
	c.limited.Store(until)
}

// limitHit logs and announces the account's usage limit, recorded in st's Usage, hit by the session by.
func (c *Coordinator) limitHit(now time.Time, s *state.Store, st state.State, by string) {
	u := st.Usage
	fields := map[string]any{"by": by, "until": u.Limited.UTC().Format(time.RFC3339), "reset_known": !u.WindowResets.IsZero(), "window": u.Window}
	if !u.WindowResets.IsZero() {
		fields["resets"] = u.WindowResets.UTC().Format(time.RFC3339)
	}
	if !u.At.IsZero() {
		fields["used_pct"] = u.UsedPct
		if !u.WeeklyResets.IsZero() {
			fields["weekly_pct"] = u.WeeklyPct
		}
	}
	s.Event("usage_limited", fields)
	c.announce(s, decide.UsageLimited(whoHit(by), u.Window, u.WindowResets, u.Limited, now))
}

// whoHit is the session by ("lead", or a worker's phase), as the human reads it.
func whoHit(by string) string {
	if by == "lead" {
		return "this session"
	}
	return "the worker on " + by
}

// runLimited is until when the run waits on the account's usage limit, as the coordinator's last step
// found it (zero: it does not).
func (c *Coordinator) runLimited() time.Time {
	if n := c.limited.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
