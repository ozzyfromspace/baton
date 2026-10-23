package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The resume timer (state/resume.go): after a usage limit whose reset is known, a run that was working on
// its own resumes as soon as the window resets, types nothing while it waits, and stops waiting the moment
// the human takes over. The coordinator arms it at the hit (usage), steps it (timer), and fires it; each
// loop's held retry is what types, once (resumeRetry): there is no second way to wake a session.

// timerHit is what a new limit hit did to the run's resume timer (armAtHit).
type timerHit int

const (
	timerNone      timerHit = iota // nothing: its reset is unknown, and each loop's retry has it
	timerArmed                     // armed, at a time it was not armed at already
	timerKept                      // as it was: armed at that time already, or cancelled already
	timerCancelled                 // cancelled: the human's own turn at the lead hit it, which paused the run
	timerPaused                    // cancelled: the run is paused, so it waits for /baton run
)

// armAtHit is what a new limit hit by the session by, recorded in st, does to the run's resume timer, which
// would fire at resume with tries counted (state.ResumeFor), at now. A run working on its own arms it
// (state.Autonomous). A limit the human's own turn at the lead hit is theirs, as a prompt of theirs while
// the timer is armed is: the run pauses, and waits for /baton run. So does a paused run. A run with nothing
// left to resume (its plan complete) arms nothing.
func armAtHit(st *state.State, by string, resume time.Time, tries int, now time.Time) timerHit {
	switch {
	case state.Autonomous(*st, by):
		if state.ArmResume(st, resume, tries) {
			return timerArmed
		}
		return timerKept
	case st.Mode == state.ModeRunning:
		state.ArmResume(st, resume, tries)
		state.CancelResume(st, state.CancelPrompt, now)
		state.Pause(st)
		return timerCancelled
	case st.Mode == state.ModePaused:
		if st.Usage.Cancelled != "" {
			return timerKept
		}
		state.ArmResume(st, resume, tries)
		state.CancelResume(st, state.CancelPause, now)
		return timerPaused
	}
	return timerNone
}

// timerHit logs and announces what a new limit hit by the session by did to the run's resume timer (t),
// recorded in st: armed, it is pushed too (usage_wait).
func (c *Coordinator) timerHit(now time.Time, s *state.Store, st state.State, by string, t timerHit) {
	u := st.Usage
	switch t {
	case timerArmed:
		line := decide.UsageWait(whoHit(by), u.Window, u.WindowResets, u.ResumeAt, now, u.Tries)
		s.Event("usage_wait", waitFields(u, by))
		s.Update(func(st *state.State) error {
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "usage_wait", Text: line, At: now})
			st.Run.Announce = append(st.Run.Announce, line)
			return nil
		})
	case timerCancelled, timerPaused:
		s.Event("usage_wait_cancelled", map[string]any{"by": u.Cancelled, "window": u.Window, "hit_by": by, "at_hit": true, "paused": t == timerCancelled})
		s.Update(func(st *state.State) error {
			st.Run.Announce = append(st.Run.Announce, decide.UsageHit(whoHit(by), u.Window, u.WindowResets, now), decide.ResumeCancelled(u.Cancelled, true))
			return nil
		})
	}
}

// waitFields are usage_wait's fields for the timer armed in u, after a limit the session by hit.
func waitFields(u *state.Usage, by string) map[string]any {
	f := map[string]any{"by": by, "window": u.Window, "resume_at": u.ResumeAt.UTC().Format(time.RFC3339)}
	if !u.WindowResets.IsZero() {
		f["resets"] = u.WindowResets.UTC().Format(time.RFC3339)
	}
	if u.Tries > 0 {
		f["tries"] = u.Tries
	}
	return f
}

// timer steps the run's resume timer, at now: it tells a cancel not told yet (a pause, state.Pause), arms a
// cancelled one again once the run is running (the human's /baton run, state.Rearm), and fires one whose
// time came (state.FireResume), which lets the loops of the sessions stopped on the limit (runs) retry, each
// once (resumeRetry). It returns the run's state as it then stands.
func (c *Coordinator) timer(now time.Time, s *state.Store, st state.State, runs []session) state.State {
	step := func(fn func(st *state.State) bool) bool {
		done := false
		if next, err := s.Update(func(st *state.State) error { done = fn(st); return nil }); err == nil {
			st = next
		}
		return done
	}
	if u := st.Usage; u != nil && st.Mode == state.ModePaused && !u.ResumeAt.IsZero() {
		step(func(st *state.State) bool {
			return st.Mode == state.ModePaused && state.CancelResume(st, state.CancelPause, time.Time{})
		})
	}
	if u := st.Usage; u != nil && u.Cancelled != "" && u.CancelSaid.IsZero() &&
		step(func(st *state.State) bool {
			if u := st.Usage; u != nil && u.Cancelled != "" && u.CancelSaid.IsZero() {
				u.CancelSaid = now
				return true
			}
			return false
		}) {
		u := st.Usage
		s.Event("usage_wait_cancelled", map[string]any{"by": u.Cancelled, "window": u.Window})
		c.announce(s, decide.ResumeCancelled(u.Cancelled, false))
	}
	if u := st.Usage; u != nil && st.Mode == state.ModeRunning && u.Cancelled != "" && step(state.Rearm) {
		if u := st.Usage; now.Before(u.ResumeAt) {
			fields := waitFields(u, u.By)
			fields["rearmed"] = true
			s.Event("usage_wait", fields)
			c.announce(s, decide.UsageWait(whoHit(u.By), u.Window, u.WindowResets, u.ResumeAt, now, u.Tries))
		}
	}
	u := st.Usage
	if u == nil || u.ResumeAt.IsZero() || now.Before(u.ResumeAt) || st.Mode != state.ModeRunning {
		return st
	}
	var woke, who []string
	for _, r := range runs {
		if hit := r.run.LimitHit(); !hit.IsZero() && hit.Before(now) {
			woke, who = append(woke, r.who), append(who, whoHit(r.who))
		}
	}
	if !step(func(st *state.State) bool { return state.FireResume(st, now) }) {
		return st
	}
	line := decide.UsageResumed(who)
	fields := map[string]any{"sessions": woke, "window": st.Usage.Window, "hit": st.Usage.Hit.UTC().Format(time.RFC3339),
		"late": now.Sub(u.ResumeAt).Round(time.Second).String()}
	if st.Usage.Tries > 0 {
		fields["tries"] = st.Usage.Tries
	}
	s.Event("usage_resumed", fields)
	if next, err := s.Update(func(st *state.State) error {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "usage_resumed", Text: line, At: now})
		st.Run.Announce = append(st.Run.Announce, line)
		return nil
	}); err == nil {
		st = next
	}
	return st
}

// resumeRetry retries a turn that ended on the usage limit (e) where the run's resume timer has it, and
// reports whether it does. Armed, or cancelled until /baton run, or the lead's run paused (a phase
// agent's loop: RunPaused), the timer holds the retry, however far off the reset is: a weekly one can be
// days away. Fired since the turn ended, the retry types once, now, through the loop's line (Loop.say),
// whatever its wait after Timing.RateLimitRetry or a sleep (wokeAt). A stop whose transcript named its
// window waits for the coordinator to record it, which arms the timer (Coordinator.usage). Anything else
// is the retry after Timing.RateLimitRetry's.
func (l *Loop) resumeRetry(v host.View, st state.State, in host.Injector, e state.StopError) bool {
	u := st.Usage
	if l.RunUsage != nil {
		u = l.RunUsage()
	}
	switch {
	case l.RunPaused != nil && l.RunPaused(), u.Waits():
		return true
	case knownWindow(e.Window) && (u == nil || u.Hit.Before(e.At)):
		return true
	case !u.FiredFor(e.At) || !l.errNudged.Before(u.Fired):
		return false
	}
	if l.why != "" {
		l.held(v, st, "retry after the usage limit reset")
		return true
	}
	l.proceed()
	if err := l.say(v, in, decide.UsageReset(st.Current)); err != nil {
		l.unsaid(err, "retrying after the usage limit reset")
		return true
	}
	l.errRetries++
	l.errNudged = v.Now
	l.Store.Event("error_retry", map[string]any{"error": e.Error, "retry": l.errRetries, "via": l.via(), "by": "resume_timer"})
	return true
}

// knownWindow reports one of the account's usage windows, as a stop's transcript names it.
func knownWindow(w string) bool { return w == state.WindowFiveHour || w == state.WindowWeekly }
