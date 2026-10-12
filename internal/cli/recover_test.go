package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
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

// The host brings the lead back when claude exited by itself mid-plan in a way nobody chose, and only then:
// it resumes the conversation, or starts a fresh session when a host started to resume it could not.
func TestWhenTheLeadComesBack(t *testing.T) {
	recovered := func(at time.Duration, fresh bool) state.Recovery {
		return state.Recovery{At: recoverT0.Add(-at), Why: "claude exited with code 137", Fresh: fresh, Told: true}
	}
	p2, p3 := "P2", "P3"
	for _, c := range []struct {
		name  string
		exit  func(*leadExit)
		st    func(*state.State)
		want  string // "resume", "fresh", "" for no recovery
		why   string
		stop  string // the escalation, when baton stops bringing it back
		fresh bool
	}{
		{name: "killed", want: "resume", why: "claude exited with code 137"},
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
			want: "resume", why: "claude exited with code 137"},
		{name: "paused", st: func(st *state.State) { st.Mode = state.ModePaused }},
		{name: "complete", st: func(st *state.State) { st.Mode = state.ModeComplete }},
		{name: "no plan", st: func(st *state.State) { st.Mode = state.ModeIdle }},
		{name: "driven by another terminal", st: func(st *state.State) { st.Owner.Instance = "inst-2" }},
		{name: "no exec here (Windows)", exit: func(x *leadExit) { x.CanExec = false }},
		{name: "no conversation to resume", st: func(st *state.State) { st.Run.SessionID = "" }},
		{name: "never started in this host", exit: func(x *leadExit) { x.Started = false }},

		{name: "once before", st: func(st *state.State) { st.Run.Recoveries = []state.Recovery{recovered(10*time.Minute, false)} },
			want: "resume", why: "claude exited with code 137"},
		{name: "twice in 30 minutes", st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(25*time.Minute, false), recovered(5*time.Minute, false)}
		}, stop: "claude exited unexpectedly 3 times in 30 minutes, so baton stopped resuming it. To go on, run `baton --resume sess-1`."},
		{name: "twice, once long ago", st: func(st *state.State) {
			st.Run.Recoveries = []state.Recovery{recovered(31*time.Minute, false), recovered(5*time.Minute, false)}
		}, want: "resume", why: "claude exited with code 137"},

		{name: "could not resume", exit: func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "claude exited with code 137" },
			st:   func(st *state.State) { st.Run.Recoveries = []state.Recovery{recovered(time.Minute, false)} },
			want: "fresh", why: "claude exited with code 137", fresh: true},
		{name: "could not resume, and P2 had a fresh session", exit: func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "claude exited with code 137" },
			st:   func(st *state.State) { st.Run.FreshFor = &p2 },
			want: "fresh", why: "claude exited with code 137", fresh: true},
		{name: "the fresh session did not start either", exit: func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "fresh: claude exited with code 137" },
			st: func(st *state.State) { st.Run.FreshFor = &p3 }, fresh: true,
			stop: "claude exited with code 1 before its session started, and baton had started a fresh session for P3 already, so it stopped trying. Once claude starts again, run `baton --resume sess-1`."},
		{name: "could not resume, at the cap", exit: func(x *leadExit) { x.Code, x.Started, x.Recovering = 1, false, "claude exited with code 137" },
			st: func(st *state.State) {
				st.Run.Recoveries = []state.Recovery{recovered(20*time.Minute, false), recovered(time.Minute, false)}
			}, fresh: true,
			stop: "claude exited unexpectedly 3 times in 30 minutes, so baton stopped resuming it. To go on, run `baton --resume sess-1`."},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, x := crashedRun(), crashed()
			if c.st != nil {
				c.st(&st)
			}
			if c.exit != nil {
				c.exit(&x)
			}
			r, ok := recoverable(st, x, "inst-1", recoverT0)
			got := ""
			if ok {
				got = "resume"
				if r.Fresh {
					got = "fresh"
				}
			}
			if got != c.want || r.Stop != c.stop {
				t.Fatalf("got %q (stop %q), want %q (stop %q)", got, r.Stop, c.want, c.stop)
			}
			if ok && r.Why != c.why || c.stop != "" && r.Fresh != c.fresh {
				t.Errorf("why %q fresh %v, want %q %v", r.Why, r.Fresh, c.why, c.fresh)
			}
		})
	}
}

// The cap counts the recoveries of the last 30 minutes, across the host's execs: claude exiting by itself
// a third time within them ends the session as before; once the oldest is 30 minutes old, it is forgotten.
func TestTheLeadComesBackTwiceIn30Minutes(t *testing.T) {
	st := crashedRun()
	st.Run.Notices = []state.Notice{{Kind: "session_ended", Text: "the session ended mid-plan (other)"}, {Kind: "note", Text: "kept"}}
	for i, step := range []struct {
		at   time.Duration
		back bool
	}{{0, true}, {10 * time.Minute, true}, {20 * time.Minute, false}, {29 * time.Minute, false}, {30 * time.Minute, true}, {35 * time.Minute, false}, {41 * time.Minute, true}} {
		now := recoverT0.Add(step.at)
		r, ok := recoverable(st, crashed(), "inst-1", now)
		if ok != step.back || !ok && !strings.Contains(r.Stop, "3 times in 30 minutes") {
			t.Fatalf("crash %d, at +%s: brought back %v (stop %q), want %v", i+1, step.at, ok, r.Stop, step.back)
		}
		if ok {
			noteRecovery(&st, r, now, "baton: "+r.Why+" mid-plan → resumed this conversation")
		}
	}
	if n := len(st.Run.Recoveries); n != 2 || !st.Run.Recoveries[0].At.Equal(recoverT0.Add(30*time.Minute)) {
		t.Errorf("recoveries kept: %+v", st.Run.Recoveries)
	}
	if len(st.Run.Announce) != 4 || st.Run.FreshFor != nil {
		t.Errorf("announced %q, fresh for %v", st.Run.Announce, st.Run.FreshFor)
	}
	if len(st.Run.Notices) != 1 || st.Run.Notices[0].Kind != "note" {
		t.Errorf("a notice that the session ended is still queued: %+v", st.Run.Notices)
	}
}

// A fresh session is recorded for the phase it starts, so a phase has one.
func TestAFreshSessionIsRecordedForItsPhase(t *testing.T) {
	st := crashedRun()
	x := crashed()
	x.Code, x.Started, x.Recovering = 1, false, "claude exited with code 137"
	r, ok := recoverable(st, x, "inst-1", recoverT0)
	if !ok || !r.Fresh {
		t.Fatalf("not fresh: %+v %v", r, ok)
	}
	noteRecovery(&st, r, recoverT0, "line")
	if st.Run.FreshFor == nil || *st.Run.FreshFor != "P3" || !st.Run.Recoveries[0].Fresh || st.Run.Recoveries[0].Why != "claude exited with code 137" {
		t.Fatalf("recorded: fresh for %v, %+v", st.Run.FreshFor, st.Run.Recoveries)
	}
	x.Recovering = freshPrefix + r.Why
	if r, ok := recoverable(st, x, "inst-1", recoverT0.Add(time.Minute)); ok || !strings.Contains(r.Stop, "fresh session for P3 already") {
		t.Errorf("a second fresh session for P3: %+v %v", r, ok)
	}
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
			resume:     []string{"baton", "--effort", "high", "--resume", id},
			freshStart: []string{"baton", "--effort", "high", "--session-id", fresh, "--", prompt}},
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
