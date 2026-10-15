package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/state"
)

var recoverT0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// crashedRun is a run whose plan runs, on P3, driven by inst-1, in the conversation sess-1.
func crashedRun() state.State {
	st := state.New()
	st.Mode, st.Current = state.ModeRunning, "P3"
	st.Phases = map[string]*state.PhaseState{"P3": {Status: state.PhaseActive, StartedAt: recoverT0.Add(-time.Hour)}}
	st.Owner = &state.Owner{Instance: "inst-1", PID: 42, Started: recoverT0.Add(-time.Hour), Heartbeat: recoverT0}
	st.Run.SessionID = "sess-1"
	return st
}

// crashed is the lead's claude killed mid-turn, after its session started in this host.
func crashed() leadExit {
	return leadExit{Exit: host.Exit{Code: 137, By: host.EndedSelf}, Started: true, CanExec: true}
}

// afreshWhy is why the loop ended the lead's claude for a fresh session, in these tests.
const afreshWhy = "the last turn failed with an API error (invalid_request: prompt is too long: 213456 tokens > 200000 maximum)"

// The host brings the lead back when claude exited by itself mid-plan in a way nobody chose, and only then:
// it resumes the conversation, or starts a fresh session when a host started to resume it could not. Past
// the cap it waits first, and it never gives up. A conversation whose last turn stands refused is resumed
// with its model left asleep, and never replaced by a fresh one. A lead the loop ended for a fresh session
// gets one, the loop having waited already.
func TestWhenTheLeadComesBack(t *testing.T) {
	recovered := func(at time.Duration, fresh bool, waited time.Duration) state.Recovery {
		return state.Recovery{At: recoverT0.Add(-at), Why: "claude exited with code 137", Fresh: fresh, Waited: waited, Told: true}
	}
	refused := func(st *state.State) {
		st.Run.LastError = &state.StopError{Error: state.ErrRefusal, Category: "cyber", At: recoverT0.Add(-2 * time.Minute)}
		st.Run.TurnStarted = recoverT0.Add(-5 * time.Minute)
	}
	afresh := func(x *leadExit) {
		x.Code, x.By, x.Afresh, x.Waited = 143, host.EndedQuit, afreshWhy, 5*time.Minute
	}
	notResumed := func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "claude exited with code 137" }
	const why = "claude exited with code 137"
	for _, c := range []struct {
		name   string
		exit   func(*leadExit)
		st     func(*state.State)
		want   string // "resume", "fresh", "" for no recovery
		why    string
		wait   time.Duration // before the host brings it back
		waited time.Duration // recorded
		quiet  bool
		afresh bool
	}{
		{name: "killed", want: "resume", why: why},
		{name: "exited with 1", exit: func(x *leadExit) { x.Code = 1 }, want: "resume", why: "claude exited with code 1"},
		{name: "exited with 0", exit: func(x *leadExit) { x.Code = 0 }},
		{name: "Ctrl-C", exit: func(x *leadExit) { x.Code = 130 }},
		{name: "terminated", exit: func(x *leadExit) { x.Code = 143 }},
		{name: "its terminal went away", exit: func(x *leadExit) { x.By, x.Code = host.EndedHangup, 129 }},
		{name: "a signal baton passed on", exit: func(x *leadExit) { x.By, x.Code = host.EndedSignal, 131 }},
		{name: "ended by baton", exit: func(x *leadExit) { x.By, x.Code = host.EndedQuit, 137 }},
		{name: "a restart on a newer baton", exit: func(x *leadExit) { x.Restart, x.By = true, host.EndedQuit }},
		{name: "the human leaving baton", exit: func(x *leadExit) { x.By = host.EndedQuit }, st: func(st *state.State) { st.Run.ExitAt = recoverT0.Add(-time.Minute) }},
		{name: "on its way out of baton", st: func(st *state.State) { st.Run.ExitAt = recoverT0.Add(-time.Minute) }},
		{name: "a deliberate SessionEnd", st: func(st *state.State) { st.Run.Ended = &state.Ended{Reason: "prompt_input_exit", At: recoverT0} }},
		{name: "logged out", st: func(st *state.State) { st.Run.Ended = &state.Ended{Reason: "logout", At: recoverT0} }},
		{name: "a SessionEnd nobody chose", st: func(st *state.State) { st.Run.Ended = &state.Ended{Reason: "other", At: recoverT0} },
			want: "resume", why: why},
		{name: "paused", st: func(st *state.State) { st.Mode = state.ModePaused }},
		{name: "complete", st: func(st *state.State) { st.Mode = state.ModeComplete }},
		{name: "no plan", st: func(st *state.State) { st.Mode = state.ModeIdle }},
		{name: "driven by another terminal", st: func(st *state.State) { st.Owner.Instance = "inst-2" }},
		{name: "no exec here (Windows)", exit: func(x *leadExit) { x.CanExec = false }},
		{name: "no conversation to resume", st: func(st *state.State) { st.Run.SessionID = "" }},
		{name: "never started in this host", exit: func(x *leadExit) { x.Started = false }},

		{name: "once before", st: func(st *state.State) { st.Run.Recoveries = []state.Recovery{recovered(10*time.Minute, false, 0)} },
			want: "resume", why: why},
		{name: "twice in 30 minutes", st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, false, 0), recovered(5*time.Minute, false, 0)}
		}, want: "resume", why: why, wait: 5 * time.Minute},
		{name: "past the cap, after a wait", st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, false, 0), recovered(5*time.Minute, false, 5*time.Minute)}
		}, want: "resume", why: why, wait: 15 * time.Minute},
		{name: "past the cap, waited already", exit: func(x *leadExit) { x.Waited = 15 * time.Minute }, st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, false, 0), recovered(20*time.Minute, false, 5*time.Minute)}
		}, want: "resume", why: why, waited: 15 * time.Minute},
		{name: "twice, once long ago", st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(31*time.Minute, false, 0), recovered(5*time.Minute, false, 0)}
		}, want: "resume", why: why},

		{name: "could not resume", exit: notResumed,
			st:   func(st *state.State) { st.Run.Recoveries = []state.Recovery{recovered(time.Minute, false, 0)} },
			want: "fresh", why: why},
		{name: "the fresh session did not start either", exit: func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "fresh: claude exited with code 137" },
			st:   func(st *state.State) { st.Run.Recoveries = []state.Recovery{recovered(time.Minute, true, 0)} },
			want: "fresh", why: why},
		{name: "could not resume, past the cap", exit: notResumed,
			st: func(st *state.State) {
				st.Run.Recoveries = []state.Recovery{recovered(20*time.Minute, false, 0), recovered(time.Minute, false, 0)}
			}, want: "fresh", why: why, wait: 5 * time.Minute},

		{name: "its last turn refused", st: refused, want: "resume", why: why, quiet: true},
		{name: "its refusal escalated", st: func(st *state.State) { st.Run.Escalation = &state.Escalation{Kind: "refused", Since: recoverT0} },
			want: "resume", why: why, quiet: true},
		{name: "refused, past the cap", st: func(st *state.State) {
			refused(st)
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, false, 0), recovered(5*time.Minute, false, 0)}
		}, want: "resume", why: why, quiet: true, wait: 5 * time.Minute},
		{name: "a refusal a turn went on from", st: func(st *state.State) {
			refused(st)
			st.Run.TurnStarted = recoverT0.Add(-time.Minute)
		}, want: "resume", why: why},
		{name: "refused, and could not resume", exit: notResumed, st: refused},

		{name: "ended for a fresh session", exit: afresh, want: "fresh", why: afreshWhy, waited: 5 * time.Minute, afresh: true},
		{name: "ended for a fresh session, past the cap", exit: afresh, st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, true, 0), recovered(5*time.Minute, true, 5*time.Minute)}
		}, want: "fresh", why: afreshWhy, waited: 5 * time.Minute, afresh: true},
		{name: "ended for a fresh session, paused meanwhile", exit: afresh, st: func(st *state.State) { st.Mode = state.ModePaused },
			want: "fresh", why: afreshWhy, waited: 5 * time.Minute, afresh: true},
		{name: "ended for a fresh session, no exec here", exit: func(x *leadExit) { afresh(x); x.CanExec = false }},
		{name: "ended for a fresh session, driven by another terminal", exit: afresh, st: func(st *state.State) { st.Owner.Instance = "inst-2" }},
		{name: "ended for a fresh session, leaving baton", exit: afresh, st: func(st *state.State) { st.Run.ExitAt = recoverT0.Add(-time.Minute) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, x := crashedRun(), crashed()
			if c.st != nil {
				c.st(&st)
			}
			if c.exit != nil {
				c.exit(&x)
			}
			r, ok := recoverable(st, x, "inst-1", recoverT0, loop.DefaultTiming)
			got := ""
			if ok {
				got = "resume"
				if r.Fresh {
					got = "fresh"
				}
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if !ok {
				return
			}
			if r.Why != c.why || r.Wait != c.wait || r.Waited != c.waited || r.Quiet != c.quiet || r.Afresh != c.afresh {
				t.Errorf("got %+v, want why %q, wait %s, waited %s, quiet %v, afresh %v", r, c.why, c.wait, c.waited, c.quiet, c.afresh)
			}
		})
	}
}

// Past the cap, each crash waits before the host brings the lead back: 5 minutes, then 15 after a wait,
// for as long as claude keeps exiting, across the host's execs. A lead that stayed up for 30 minutes
// starts over. Each recovery records what it waited, and a notice that the session ended goes.
func TestTheLeadComesBackWithBackoffPastTheCap(t *testing.T) {
	st := crashedRun()
	st.Run.Notices = []state.Notice{{Kind: "session_ended", Text: "the session ended mid-plan (other)"}, {Kind: "note", Text: "kept"}}
	now := recoverT0
	for i, step := range []struct {
		after time.Duration // up since the last time it was brought back
		wait  time.Duration
	}{{0, 0}, {10 * time.Minute, 0}, {time.Minute, 5 * time.Minute}, {time.Minute, 15 * time.Minute}, {time.Minute, 15 * time.Minute},
		{time.Minute, 15 * time.Minute}, {36 * time.Minute, 0}, {time.Minute, 0}, {time.Minute, 5 * time.Minute}} {
		now = now.Add(step.after)
		x := crashed()
		r, ok := recoverable(st, x, "inst-1", now, loop.DefaultTiming)
		if !ok || r.Wait != step.wait {
			t.Fatalf("crash %d, at +%s: brought back %v, after %s; want after %s", i+1, now.Sub(recoverT0), ok, r.Wait, step.wait)
		}
		if r.Wait > 0 { // the host waits, then looks again
			now, x.Waited = now.Add(r.Wait), r.Wait
			if r, ok = recoverable(st, x, "inst-1", now, loop.DefaultTiming); !ok || r.Wait != 0 || r.Waited != step.wait {
				t.Fatalf("crash %d, after its wait: %+v %v", i+1, r, ok)
			}
		}
		noteRecovery(&st, r, now, "baton: "+r.Why+" mid-plan → resumed this conversation")
		if got := st.Run.Recoveries[len(st.Run.Recoveries)-1]; !got.At.Equal(now) || got.Waited != step.wait {
			t.Fatalf("crash %d recorded %+v", i+1, got)
		}
	}
	if len(st.Run.Announce) != 9 {
		t.Errorf("announced %q", st.Run.Announce)
	}
	if len(st.Run.Notices) != 1 || st.Run.Notices[0].Kind != "note" {
		t.Errorf("a notice that the session ended is still queued: %+v", st.Run.Notices)
	}
}

// What the host brings back is recorded as it is: a fresh session, a quiet resume.
func TestARecoveryIsRecordedAsItIs(t *testing.T) {
	for _, r := range []recovery{
		{Why: "claude exited with code 1", Fresh: true},
		{Why: "claude exited with code 1", Quiet: true, Waited: 5 * time.Minute},
		{Why: afreshWhy, Fresh: true, Afresh: true, Waited: 15 * time.Minute},
	} {
		st := crashedRun()
		noteRecovery(&st, r, recoverT0, "line")
		want := state.Recovery{At: recoverT0, Why: r.Why, Fresh: r.Fresh, Waited: r.Waited, Quiet: r.Quiet}
		if len(st.Run.Recoveries) != 1 || st.Run.Recoveries[0] != want || !slices.Equal(st.Run.Announce, []string{"line"}) {
			t.Errorf("%+v recorded %+v, announced %q", r, st.Run.Recoveries, st.Run.Announce)
		}
	}
}

// pushed records what the host pushes to the human.
type pushed struct{ kinds, texts []string }

func (p *pushed) Notify(_, kind, text string) error {
	p.kinds, p.texts = append(p.kinds, kind), append(p.texts, text)
	return nil
}

// A host that must wait before it brings the lead back says so, in its terminal and by a push, and logs
// it. Stopped there by the human (Ctrl-C), it lets the lead go, with the escalation that says how to go on;
// and if the human resumed the conversation in another terminal meanwhile, it lets the run go to that host.
// Neither brings claude back here.
func TestTheHostWaitsPastTheCap(t *testing.T) {
	for _, c := range []struct {
		name string
		stop bool // the human pressed Ctrl-C
		took bool // another terminal's host took the run over meanwhile
		want string
	}{
		{name: "stopped by the human", stop: true, want: decide.RecoveryStopped("sess-1")},
		{name: "taken over meanwhile", took: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := recoverT0
			clock := func() time.Time { return now }
			proj, err := state.OpenProject(t.TempDir(), clock)
			if err != nil {
				t.Fatal(err)
			}
			s, err := proj.Bind(state.Binding{Session: "sess-1", Instance: "inst-1"})
			if err != nil {
				t.Fatal(err)
			}
			started := now.Add(-time.Hour)
			s.Update(func(st *state.State) error {
				*st = crashedRun()
				st.Run.LastActivity = now.Add(-time.Minute) // its session started in this host
				st.Run.Recoveries = []state.Recovery{{At: now.Add(-20 * time.Minute)}, {At: now.Add(-5 * time.Minute)}}
				st.Run.Notices = []state.Notice{{Kind: "session_ended", Text: "the session ended mid-plan (other)", At: now}}
				return nil
			})
			var waited []time.Duration
			defer func(p func(time.Duration) bool) { pause = p }(pause)
			pause = func(d time.Duration) bool {
				waited = append(waited, d)
				now = now.Add(d)
				if c.took {
					s.Update(func(st *state.State) error { st.Owner = &state.Owner{Instance: "inst-2", Heartbeat: now}; return nil })
				}
				return !c.stop
			}
			var out bytes.Buffer
			io := IO{Out: &out, Err: &out, Env: func(string) string { return "" }, Now: clock}
			push := &pushed{}
			lead := &loop.Loop{Store: s, Notify: push, Project: "demo", Timing: loop.DefaultTiming}
			code, handled, stop := recoverLead(io, proj, "inst-1", filepath.Join(t.TempDir(), "no-baton"), nil, crashed(), started, loop.DefaultTiming,
				&loop.Coordinator{Lead: lead}, lead, func(string, ...any) {})
			if code != 0 || handled || stop != c.want {
				t.Fatalf("recoverLead: %d %v %q, want the stop %q", code, handled, stop, c.want)
			}
			if !slices.Equal(waited, []time.Duration{5 * time.Minute}) {
				t.Errorf("waited %v", waited)
			}
			if !strings.Contains(out.String(), "baton: claude exited with code 137 mid-plan, 3 times in 30 minutes; baton brings it back at ") {
				t.Errorf("the terminal: %q", out.String())
			}
			if !slices.Equal(push.kinds, []string{"recovery_backoff"}) || !strings.Contains(push.texts[0], "so baton waits before it brings it back") {
				t.Errorf("pushed %v %q", push.kinds, push.texts)
			}
			events := readEvents(t, s)
			if !hasEvent(events, "recovery_backoff", map[string]any{"wait": "5m0s", "times": float64(3), "why": "claude exited with code 137"}) {
				t.Errorf("events: %v", events)
			}
			if got := hasEvent(events, "lead_exited", map[string]any{"then": "stop", "by": "human"}); got != c.stop {
				t.Errorf("lead_exited, then stop: %v; events %v", got, events)
			}
			st, _ := s.Load()
			if len(st.Run.Recoveries) != 2 || len(st.Run.Announce) != 0 {
				t.Errorf("recorded a recovery that did not happen: %+v, %q", st.Run.Recoveries, st.Run.Announce)
			}
		})
	}
}

// readEvents is the run's event log.
func readEvents(t *testing.T, s *state.Store) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// hasEvent reports an event of kind with every one of fields.
func hasEvent(events []map[string]any, kind string, fields map[string]any) bool {
	return slices.ContainsFunc(events, func(e map[string]any) bool {
		if e["kind"] != kind {
			return false
		}
		for k, v := range fields {
			if e[k] != v {
				return false
			}
		}
		return true
	})
}

// The host that brings the lead back runs with the human's own claude arguments, the session chosen anew:
// the conversation resumed, or a fresh session under the id baton linked to the run, with its prompt.
func TestTheExecsThatBringTheLeadBack(t *testing.T) {
	const id, fresh = "11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	prompt := "[baton] claude exited with code 137 mid-plan and its conversation could not be resumed"
	for _, c := range []struct {
		name       string
		args       []string
		resume     []string
		freshStart []string
	}{
		{name: "a session baton started", args: []string{"--model", "opus", "--permission-mode", "auto", "--session-id", id, "--", "fix the bug"},
			resume:     []string{"baton", "--model", "opus", "--permission-mode", "auto", "--resume", id},
			freshStart: []string{"baton", "--model", "opus", "--permission-mode", "auto", "--session-id", fresh, "--", prompt}},
		{name: "a resumed session", args: []string{"--resume", id, "--dangerously-skip-permissions", "--autocompact", "500k"},
			resume:     []string{"baton", "--dangerously-skip-permissions", "--autocompact", "500k", "--resume", id},
			freshStart: []string{"baton", "--dangerously-skip-permissions", "--autocompact", "500k", "--session-id", fresh, "--", prompt}},
		{name: "a continued one", args: []string{"--continue", "--effort", "high", "--allowedTools", "Edit"},
			resume:     []string{"baton", "--effort", "high", "--allowedTools", "Edit", "--resume", id},
			freshStart: []string{"baton", "--effort", "high", "--allowedTools", "Edit", "--session-id", fresh, "--", prompt}},
		{name: "the picker", args: []string{"-r", "--model=haiku"},
			resume:     []string{"baton", "--model=haiku", "--resume", id},
			freshStart: []string{"baton", "--model=haiku", "--session-id", fresh, "--", prompt}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := recoverArgv(c.args, id, false, ""); !slices.Equal(got, c.resume) {
				t.Errorf("resume: %q, want %q", got, c.resume)
			}
			got := recoverArgv(c.args, fresh, true, prompt)
			if !slices.Equal(got, c.freshStart) {
				t.Errorf("fresh: %q, want %q", got, c.freshStart)
			}
			// The next host runs its session under the id baton linked, and passes the prompt on.
			if session, args := sessionOf(got[1:]); session != fresh || !slices.Equal(args, got[1:]) {
				t.Errorf("the next host's session: %q, args %q", session, args)
			}
		})
	}

	env := []string{"PATH=/bin", "BATON_RECOVER=claude exited with code 137", "BATON_HOME=/tmp/bh", "HOME=/home/h"}
	for _, c := range []struct {
		r    recovery
		want string
	}{
		{recovery{Why: "claude exited with code 1"}, "BATON_RECOVER=claude exited with code 1"},
		{recovery{Why: "claude exited with code 137", Fresh: true}, "BATON_RECOVER=fresh: claude exited with code 137"},
	} {
		got := recoverEnv(env, c.r)
		var recovers []string
		for _, kv := range got {
			if strings.HasPrefix(kv, "BATON_RECOVER=") {
				recovers = append(recovers, kv)
			}
		}
		if !slices.Equal(recovers, []string{c.want}) || !slices.Contains(got, "PATH=/bin") || !slices.Contains(got, "BATON_HOME=/tmp/bh") || len(got) != len(env) {
			t.Errorf("env %q, want it with %q", got, c.want)
		}
	}
}
