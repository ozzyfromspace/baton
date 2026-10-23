package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A turn of any session of the run that ends on the account's usage limit holds the whole run: no worker
// starts until the limit resets, a minute after the reset of the window the stop names (its transcript,
// StopError.Window), else of a window the status line has at 99% or more, the weekly one first, or after
// RateLimitRetry when when it resets is unknown. One limit hit is logged and announced once.
func TestAUsageLimitHitInAnySessionHoldsTheRun(t *testing.T) {
	for _, c := range []struct {
		name   string
		by     string        // the session that hits it: "lead", or a worker's phase
		window string        // the window the stop's transcript names ("" none)
		used   float64       // the status line's reading of the five-hour window (0: none)
		weekly float64       // and of the weekly one (0: none)
		resets time.Duration // from the hit, of the window that is the limit
		until  time.Duration // from the hit
		known  string        // the window recorded
	}{
		{"a worker, its reset unknown", "P1", "", 0, 0, 0, DefaultTiming.RateLimitRetry, state.WindowUnknown},
		{"a worker, the five-hour window used up", "P1", "", 100, 0, 2 * time.Hour, 2*time.Hour + limitGrace, state.WindowFiveHour},
		{"the lead, the five-hour window used up", "lead", "", 100, 0, 40 * time.Minute, 40*time.Minute + limitGrace, state.WindowFiveHour},
		{"the lead, the five-hour window read at 99", "lead", "", 99, 0, 40 * time.Minute, 40*time.Minute + limitGrace, state.WindowFiveHour},
		{"the lead, the window not used up: another limit", "lead", "", 62, 0, 2 * time.Hour, DefaultTiming.RateLimitRetry, state.WindowUnknown},
		{"a worker, the weekly window its transcript names", "P1", state.WindowWeekly, 27, 0, 104 * time.Hour, 104*time.Hour + limitGrace, state.WindowWeekly},
		{"the lead, the weekly window read at 99", "lead", "", 27, 99.2, 30 * time.Hour, 30*time.Hour + limitGrace, state.WindowWeekly},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 2)
			x.tick(time.Second)
			if len(x.spawns()) != 1 {
				t.Fatalf("%d workers started, want P1's", len(x.spawns()))
			}
			hit := x.now
			if c.used > 0 || c.weekly > 0 {
				x.lead.Update(func(st *state.State) error {
					st.Usage = &state.Usage{UsedPct: c.used, Resets: hit.Add(min(c.resets, 2*time.Hour)), At: hit}
					if c.weekly > 0 {
						st.Usage.WeeklyPct, st.Usage.WeeklyResets = c.weekly, hit.Add(c.resets)
					}
					return nil
				})
			}
			stop := state.StopError{Error: "rate_limit", At: hit}
			if c.window != "" {
				stop.Window, stop.Resets = c.window, hit.Add(c.resets)
			}
			at := x.lead
			if c.by != "lead" {
				at = x.sub(c.by)
			}
			at.Update(func(st *state.State) error {
				st.Run.LastError = &stop
				return nil
			})
			x.tick(time.Second)
			until := hit.Add(c.until)
			u := x.state().Usage
			if u == nil || !u.Limited.Equal(until) || !u.Hit.Equal(hit) || u.By != c.by || u.Window != c.known {
				t.Fatalf("usage %+v, want limited until %s by %s", u, until, c.by)
			}
			if got := x.c.runLimited(); !got.Equal(until) {
				t.Errorf("the workers' Loops read %s", got)
			}
			ev := x.events()
			if strings.Count(ev, `"kind":"usage_limited"`) != 1 || !strings.Contains(ev, `"by":"`+c.by+`"`) || !strings.Contains(ev, `"window":"`+c.known+`"`) {
				t.Errorf("events:\n%s", ev)
			}
			who := "this session"
			if c.by != "lead" {
				who = "the worker on " + c.by
			}
			ann := strings.Join(x.state().Run.Announce, "\n")
			if !strings.Contains(ann, who+" hit the account's ") || !strings.Contains(ann, "until "+decide.Clock(until, hit)) ||
				strings.Contains(ann, "resets is unknown") != (c.known == state.WindowUnknown) {
				t.Errorf("announced %q", ann)
			}

			// Until then, no worker starts, and the same hit is not recorded again.
			step := max(time.Minute, c.until/60) // a weekly hold is days long
			for x.now.Add(step).Before(until) {
				x.tick(step)
			}
			x.tick(until.Sub(x.now) - time.Second)
			if len(x.spawns()) != 1 || strings.Count(x.events(), `"kind":"usage_limited"`) != 1 {
				t.Fatalf("%d workers started during the limit; events:\n%s", len(x.spawns()), x.events())
			}
			x.tick(2 * time.Second)
			if got := x.spawns(); len(got) != 2 || got[1] != "P2" {
				t.Fatalf("no worker started once the limit reset: %d", len(x.spawns()))
			}

			// The next turn that ends on the limit is a new hit.
			again := x.now
			at.Update(func(st *state.State) error {
				st.Run.TurnStarted = again.Add(-time.Second)
				st.Run.LastError = &state.StopError{Error: "rate_limit", At: again}
				return nil
			})
			x.tick(time.Second)
			if u := x.state().Usage; !u.Hit.Equal(again) || strings.Count(x.events(), `"kind":"usage_limited"`) != 2 {
				t.Errorf("a second hit: %+v", u)
			}
		})
	}
}

// A turn that ended on the limit and a turn started since are not a limit hit; nor is any other error.
func TestLimitHit(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		r    state.Runtime
		want time.Time
	}{
		{"none", state.Runtime{}, time.Time{}},
		{"the last turn ended on it", state.Runtime{TurnStarted: at.Add(-time.Minute), LastError: &state.StopError{Error: "rate_limit", At: at}}, at},
		{"a turn started since", state.Runtime{TurnStarted: at.Add(time.Second), LastError: &state.StopError{Error: "rate_limit", At: at}}, time.Time{}},
		{"another error", state.Runtime{LastError: &state.StopError{Error: "overloaded", At: at}}, time.Time{}},
	} {
		if got := c.r.LimitHit(); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// While the run waits on the account's usage limit, every session's watchdog stays quiet: no retry, no
// reminder, no compaction typed, and nothing escalated as stalled. Claude Code resumes each session
// itself as the limit resets; whatever baton still owes a session goes out once the hold is over.
func TestTheWatchdogStaysQuietWhileTheRunIsLimited(t *testing.T) {
	const limit = 50 * time.Minute
	for _, c := range []struct {
		name  string
		set   func(r *rig, st *state.State)
		after string // what baton types once the hold is over ("": it escalates the stall)
	}{
		{"the lead's turn ended on the limit", func(r *rig, st *state.State) {
			st.Run.LastError = &state.StopError{Error: "rate_limit", At: r.now}
		}, "API error (rate_limit)"},
		{"the lead, idle", nil, "No activity"},
		{"the lead, idle, reminded as often as it is", func(r *rig, st *state.State) { st.Run.IdleNudges = MaxIdleNudges }, ""},
		{"a compaction queued", func(r *rig, st *state.State) {
			st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "boundary", Queued: r.now}
		}, "/compact"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIdleRig(t)
			until := r.now.Add(limit)
			r.set(func(st *state.State) { st.Usage = &state.Usage{Limited: until, Hit: r.now, By: "lead"} })
			if c.set != nil {
				r.set(func(st *state.State) { c.set(r, st) })
			}
			for r.now.Before(until.Add(-time.Minute)) {
				r.tick(time.Minute)
			}
			st := r.state()
			if len(r.in.text) != 0 || st.Run.Escalation != nil || st.Run.Gate != gateUsage && strings.Contains(c.after, "compact") {
				t.Fatalf("during the limit: typed %q, escalation %+v, gate %q", r.in.text, st.Run.Escalation, st.Run.Gate)
			}
			for i := 0; i < 12 && len(r.in.text) == 0 && r.state().Run.Escalation == nil; i++ {
				r.tick(time.Minute)
			}
			if c.after == "" {
				if e := r.state().Run.Escalation; e == nil || e.Kind != "stalled" || len(r.in.text) != 0 {
					t.Fatalf("after the limit: escalation %+v, typed %q", e, r.in.text)
				}
				return
			}
			if len(r.in.text) == 0 || !strings.Contains(r.in.text[0], c.after) {
				t.Fatalf("after the limit: typed %q, want %q", r.in.text, c.after)
			}
		})
	}
}
