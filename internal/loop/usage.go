package loop

import (
	"fmt"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// limitGrace is how long after the account's usage limit resets the run stays held: Claude Code resumes
// each session by itself as the limit resets, and baton types nothing over that.
const limitGrace = time.Minute

// usage notices a session of the run whose last turn ended on the account's usage limit. Every session
// of the run shares the account, so until the limit resets no worker starts, and nothing is typed into
// any of them (gateUsage): each session's Claude Code waits for the reset by itself. The reset is the
// five-hour window's when the status line has it used up; otherwise baton cannot tell when it is, and
// looks again after Timing.RateLimitRetry, as for one session (watchError). A limit hit is recorded once
// (state.Limit), and the workers' Loops read it from here.
func (c *Coordinator) usage(now time.Time, s *state.Store, st state.State) {
	by, at := "lead", st.Run.LimitHit()
	for _, w := range c.launched() {
		if sub, err := w.store.Load(); err == nil {
			if hit := sub.Run.LimitHit(); hit.After(at) {
				by, at = w.phase, hit
			}
		}
	}
	if !at.IsZero() {
		until, known := at.Add(c.Lead.Timing.RateLimitRetry), false
		if u := st.Usage; u != nil && u.UsedPct >= 100 && u.Resets.After(at) {
			until, known = u.Resets.Add(limitGrace), true
		}
		hit := false
		if next, err := s.Update(func(st *state.State) error { hit = state.Limit(st, by, at, until); return nil }); err == nil {
			st = next
		}
		if hit {
			c.limitHit(s, st, by, until, known)
		}
	}
	var until int64
	if u := st.Usage; u != nil && !u.Limited.IsZero() {
		until = u.Limited.UnixNano()
	}
	c.limited.Store(until)
}

// limitHit logs and announces the account's usage limit, hit by the session by, which holds the run until
// until (known: the reset is the five-hour window's).
func (c *Coordinator) limitHit(s *state.Store, st state.State, by string, until time.Time, known bool) {
	fields := map[string]any{"by": by, "until": until.UTC().Format(time.RFC3339), "reset_known": known}
	if u := st.Usage; u != nil && !u.At.IsZero() {
		fields["used_pct"] = u.UsedPct
	}
	s.Event("usage_limited", fields)
	who := "the worker on " + by
	if by == "lead" {
		who = "this session"
	}
	when := fmt.Sprintf("until %s, a minute after it resets: Claude Code resumes each session itself", until.Local().Format("15:04"))
	if !known {
		when = fmt.Sprintf("until %s: when it resets is unknown, so baton looks again then", until.Local().Format("15:04"))
	}
	c.announce(s, fmt.Sprintf("baton: %s hit the account's usage limit. No worker starts, and baton types nothing into the run's sessions, %s", who, when))
}

// runLimited is until when the run waits on the account's usage limit, as the coordinator's last step
// found it (zero: it does not).
func (c *Coordinator) runLimited() time.Time {
	if n := c.limited.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
