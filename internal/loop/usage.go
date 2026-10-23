package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// limitGrace is how long after the account's usage limit resets the run stays held, and its resume timer
// fires (state.ResumeGrace). Nothing resumes a stopped session by itself (docs/research/usage.md): once the
// hold ends, each loop's own retry does (watchError), at the timer's word where it has one (resume.go).
const limitGrace = state.ResumeGrace

// session is a session of the run as the usage step reads it: who it is ("lead", or a worker's phase) and
// its runtime.
type session struct {
	who string
	run state.Runtime
}

// usage notices a session of the run whose last turn ended on one of the account's usage limits. Every
// session of the run shares the account, so until the limit resets no worker starts, and nothing is typed
// into any of them (gateUsage). Which window it was, and when that resets, is the stop's own word from its
// transcript, else a status line reading at the limit's line (state.LimitWindow). With the reset known, a
// run working on its own arms the resume timer at it (resume.go); when it is unknown, each loop retries
// after Timing.RateLimitRetry (watchError). A limit hit is recorded once (state.RecordLimit), and the
// workers' Loops read it from here. A session that got a reply since the hit lifts the hold
// (state.LiftLimit). The coordinator's duties call it, so a run with no workers holds too.
func (c *Coordinator) usage(now time.Time, s *state.Store, st state.State) {
	by, at, stop := "lead", st.Run.LimitHit(), st.Run.LastError
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
		hit := false
		var t timerHit
		if next, err := s.Update(func(st *state.State) error {
			resume, tries := state.ResumeFor(st.Usage, at, resets)
			until := at.Add(c.Lead.Timing.RateLimitRetry)
			if !resume.IsZero() {
				until = resume
			}
			if hit = state.RecordLimit(st, by, at, until, window, resets); hit && !resume.IsZero() {
				t = armAtHit(st, by, resume, tries, now)
			}
			return nil
		}); err == nil {
			st = next
		}
		if hit {
			c.limitHit(now, s, st, by, t)
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
	st = c.windDown(now, s, st)
	st = c.timer(now, s, st, runs)
	var u *state.Usage
	if st.Usage != nil {
		cp := *st.Usage
		u = &cp
	}
	c.run.Store(u)
	c.paused.Store(st.Mode == state.ModePaused)
}

// limitHit logs and announces the account's usage limit, recorded in st's Usage, hit by the session by,
// and what the hit did to the resume timer (t).
func (c *Coordinator) limitHit(now time.Time, s *state.Store, st state.State, by string, t timerHit) {
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
	if t == timerNone {
		c.announce(s, decide.UsageLimited(whoHit(by), u.Window, u.WindowResets, u.Limited, now))
		return
	}
	c.timerHit(now, s, st, by, t)
}

// whoHit is the session by ("lead", or a worker's phase), as the human reads it.
func whoHit(by string) string {
	if by == "lead" {
		return "this session"
	}
	return "the worker on " + by
}

// runUsage is the account's usage, its limit and its resume timer, as the coordinator's last step of usage
// found them (nil: none recorded).
func (c *Coordinator) runUsage() *state.Usage { return c.run.Load() }

// runLimited is until when the run waits on the account's usage limit, as the coordinator's last step
// found it (zero: it does not).
func (c *Coordinator) runLimited() time.Time {
	if u := c.run.Load(); u != nil {
		return u.Limited
	}
	return time.Time{}
}
