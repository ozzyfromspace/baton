package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A turn of any session of the run that ends on the account's usage limit holds the whole run: no worker
// starts until the limit resets, a minute after the five-hour window's reset when the status line has it
// used up, or after RateLimitRetry when when it resets is unknown. One limit hit is logged and announced
// once.
func TestAUsageLimitHitInAnySessionHoldsTheRun(t *testing.T) {
	for _, c := range []struct {
		name   string
		by     string        // the session that hits it: "lead", or a worker's phase
		used   float64       // the status line's reading of the five-hour window (0: none)
		resets time.Duration // from the hit
		until  time.Duration // from the hit
		known  bool
	}{
		{"a worker, its reset unknown", "P1", 0, 0, DefaultTiming.RateLimitRetry, false},
		{"a worker, the five-hour window used up", "P1", 100, 2 * time.Hour, 2*time.Hour + limitGrace, true},
		{"the lead, the five-hour window used up", "lead", 100, 40 * time.Minute, 40*time.Minute + limitGrace, true},
		{"the lead, the window not used up: another limit", "lead", 62, 2 * time.Hour, DefaultTiming.RateLimitRetry, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 2)
			x.tick(time.Second)
			if len(x.specs) != 1 {
				t.Fatalf("%d workers started, want P1's", len(x.specs))
			}
			hit := x.now
			if c.used > 0 {
				x.lead.Update(func(st *state.State) error {
					st.Usage = &state.Usage{UsedPct: c.used, Resets: hit.Add(c.resets), At: hit}
					return nil
				})
			}
			at := x.lead
			if c.by != "lead" {
				at = x.sub(c.by)
			}
			at.Update(func(st *state.State) error {
				st.Run.LastError = &state.StopError{Error: "rate_limit", At: hit}
				return nil
			})
			x.tick(time.Second)
			until := hit.Add(c.until)
			u := x.state().Usage
			if u == nil || !u.Limited.Equal(until) || !u.Hit.Equal(hit) || u.By != c.by {
				t.Fatalf("usage %+v, want limited until %s by %s", u, until, c.by)
			}
			if got := x.c.runLimited(); !got.Equal(until) {
				t.Errorf("the workers' Loops read %s", got)
			}
			ev := x.events()
			if strings.Count(ev, `"kind":"usage_limited"`) != 1 || !strings.Contains(ev, `"by":"`+c.by+`"`) {
				t.Errorf("events:\n%s", ev)
			}
			who := "this session"
			if c.by != "lead" {
				who = "the worker on " + c.by
			}
			ann := strings.Join(x.state().Run.Announce, "\n")
			if !strings.Contains(ann, who+" hit the account's usage limit") || !strings.Contains(ann, "until "+until.Local().Format("15:04")) ||
				strings.Contains(ann, "resets is unknown") == c.known {
				t.Errorf("announced %q", ann)
			}

			// Until then, no worker starts, and the same hit is not recorded again.
			for x.now.Add(time.Minute).Before(until) {
				x.tick(time.Minute)
			}
			x.tick(until.Sub(x.now) - time.Second)
			if len(x.specs) != 1 || strings.Count(x.events(), `"kind":"usage_limited"`) != 1 {
				t.Fatalf("%d workers started during the limit; events:\n%s", len(x.specs), x.events())
			}
			x.tick(2 * time.Second)
			if len(x.specs) != 2 || x.specs[1].Name != "P2" {
				t.Fatalf("no worker started once the limit reset: %d", len(x.specs))
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
		name   string
		worker bool
		set    func(r *rig, st *state.State)
		after  string // what baton types once the hold is over ("": it escalates the stall)
	}{
		{"the lead's turn ended on the limit", false, func(r *rig, st *state.State) {
			st.Run.LastError = &state.StopError{Error: "rate_limit", At: r.now}
		}, "API error (rate_limit)"},
		{"the lead, idle", false, nil, "No activity"},
		{"a worker's turn ended on the limit", true, func(r *rig, st *state.State) {
			st.Run.LastError = &state.StopError{Error: "rate_limit", At: r.now}
		}, "API error (rate_limit)"},
		{"a worker, idle", true, nil, "No activity"},
		{"the lead, idle, reminded as often as it is", false, func(r *rig, st *state.State) { st.Run.IdleNudges = MaxIdleNudges }, ""},
		{"a compaction queued", false, func(r *rig, st *state.State) {
			st.Run.Compaction = state.Compaction{Epoch: 2, Status: state.CompactQueued, Reason: "boundary", Queued: r.now}
		}, "/compact"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIdleRig(t)
			until := r.now.Add(limit)
			if c.worker {
				r.asWorker()
				r.coord.limited.Store(until.UnixNano())
			} else {
				r.set(func(st *state.State) { st.Usage = &state.Usage{Limited: until, Hit: r.now, By: "lead"} })
			}
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
