package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// windWait is why a wound-down run with nothing under way is not stalled: it waits for the weekly reset.
const windWait = "the run winds down until the weekly reset"

// woundUpAt is when the run st last went on after a wind-down (zero: never): the stall alarm counts quiet
// from it.
func woundUpAt(st state.State) time.Time {
	if st.Usage == nil {
		return time.Time{}
	}
	return st.Usage.WoundUp
}

// windDown steps the run's weekly wind-down (state/winddown.go) at now, before the resume timer's step, and
// returns the run's state as it then stands. A wound-down run goes on once the weekly reset it waited for
// has passed, or a reading below the line came, or the reserve is off, and the lead takes up its next phase
// after a compaction; a pause of a wound-down run is told once, as is its going on again once /baton run
// gives it back; and the run winds down at the line (WeeklyReserve), once a weekly window. Each is
// announced and logged, and winding down and going on are pushed too.
func (c *Coordinator) windDown(now time.Time, s *state.Store, st state.State) state.State {
	step := func(fn func(st *state.State) bool) bool {
		done := false
		if next, err := s.Update(func(st *state.State) error { done = fn(st); return nil }); err == nil {
			st = next
		}
		return done
	}
	if by := state.WindUpBy(st, c.WeeklyReserve, now); by != "" {
		if pl, err := s.LoadPlan(); err == nil {
			was, took, epoch := *st.Usage, "", 0
			if step(func(st *state.State) bool {
				if state.WindUpBy(*st, c.WeeklyReserve, now) != by {
					return false
				}
				if took = state.WindUp(st, pl, now); took != "" {
					st.Run.Compaction.Queue("boundary", now)
					epoch = st.Run.Compaction.Epoch
				}
				return true
			}) {
				c.woundUp(now, s, was, by, took, epoch)
			}
		}
	}
	if st.WoundDown() && st.Mode == state.ModePaused && step(func(st *state.State) bool { return state.WindPause(st, now) }) {
		s.Event("usage_wait_cancelled", map[string]any{"by": state.CancelPause, "window": state.WindowWeekly, "wind_down": true})
		if st.Usage.Cancelled == "" { // else the resume timer's own line says the run waits for /baton run
			c.announce(s, decide.WindCancelled(state.CancelPause))
		}
	}
	if st.WindArmed() && step(state.WindRearm) {
		u := st.Usage
		s.Event("usage_wait", map[string]any{"by": "wind_down", "window": state.WindowWeekly, "resume_at": u.GoesOnAt().UTC().Format(time.RFC3339), "rearmed": true})
		if !(u.Cancelled != "" && u.Window == state.WindowWeekly) { // else the timer's own line names the same reset
			c.announce(s, decide.WindRearmed(u.GoesOnAt(), now))
		}
	}
	if (st.Mode == state.ModeRunning || st.Mode == state.ModePaused) && step(func(st *state.State) bool { return state.WindDown(st, c.WeeklyReserve, now) }) {
		c.woundDown(now, s, st)
		if st.Mode == state.ModePaused {
			step(func(st *state.State) bool { return state.WindPause(st, now) }) // the wind-down's own line says so
		}
	}
	return st
}

// woundDown logs, announces and pushes the run st winding down at now: nothing new starts, and what is under
// way finishes.
func (c *Coordinator) woundDown(now time.Time, s *state.Store, st state.State) {
	u := st.Usage
	var finishing []string
	if pl, err := s.LoadPlan(); err == nil {
		finishing = state.UnderWay(st, pl)
	}
	paused := st.Mode == state.ModePaused
	line := decide.WindDown(u.WeeklyPct, finishing, u.GoesOnAt(), paused, now)
	s.Event("weekly_wind_down", map[string]any{"weekly_pct": u.WeeklyPct, "reserve": c.WeeklyReserve, "resets": u.WindFor.UTC().Format(time.RFC3339),
		"goes_on_at": u.GoesOnAt().UTC().Format(time.RFC3339), "finishing": finishing, "paused": paused})
	c.Lead.logf("loop: weekly usage at %.1f%%: the run winds down until %s", u.WeeklyPct, u.WindFor.Format(time.RFC3339))
	s.Update(func(st *state.State) error {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "weekly_wind_down", Text: line, At: now})
		st.Run.Announce = append(st.Run.Announce, line)
		return nil
	})
}

// woundUp logs, announces and pushes the run going on at now after the wind-down was recorded (was), by
// why (state.WindReset, …), the lead taking up took after the compaction queued at epoch.
func (c *Coordinator) woundUp(now time.Time, s *state.Store, was state.Usage, by, took string, epoch int) {
	fields := map[string]any{"by": by, "wound_down": was.WoundDown.UTC().Format(time.RFC3339), "resets": was.WindFor.UTC().Format(time.RFC3339)}
	if by == state.WindReset {
		fields["late"] = now.Sub(was.GoesOnAt()).Round(time.Second).String()
	}
	if took != "" {
		fields["took"] = took
	}
	s.Event("weekly_wound_up", fields)
	if took != "" {
		s.Event("compact_queued", map[string]any{"reason": "boundary", "epoch": epoch, "next": took, "by": "weekly_wound_up"})
	}
	c.Lead.logf("loop: the weekly wind-down ends (%s)", by)
	line := decide.WoundUp(by, took)
	s.Update(func(st *state.State) error {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "weekly_wound_up", Text: line, At: now})
		st.Run.Announce = append(st.Run.Announce, line)
		return nil
	})
}
