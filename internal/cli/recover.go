package cli

import (
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/upgrade"
)

// A lead whose claude exits by itself mid-plan is brought back by its host (docs/research/recovery.md):
// the host execs a new host on the same binary, as a restart on a newer baton does, which resumes the
// conversation; when that cannot be done (its claude exits before its session starts), the next one
// starts a fresh session from the phase's brief. The same goes for a lead whose loop ended its claude
// for a fresh session, after an API error that every turn of its conversation would meet again
// (state.StopError.Afresh). The new host's session learns why from BATON_RECOVER, and the run's state
// (Runtime.Recoveries) says what to tell its model, once.
//
// Nothing gives up on the lead while its plan runs: past state.MaxRecoveries in state.RecoveryWindow, the
// host stays, tells the human, and brings it back after a wait (state.Backoff: Timing.RecoverAfter,
// then every Timing.RecoverEvery), unless the human stops it there (Ctrl-C) or resumes the
// conversation from another terminal first. A conversation whose last turn stands refused is not
// crashed: it is resumed with its model left asleep, and no fresh session replaces it, since how a
// refused turn goes on is the human's.

// freshPrefix starts BATON_RECOVER for a fresh session: "fresh: claude exited with code 137".
const freshPrefix = "fresh: "

// leadExit is how the lead's claude ended, as the host saw it.
type leadExit struct {
	host.Exit
	Restart bool // the host ended it to restart the session on a newer baton
	// Afresh is why the loop ended it for a fresh session (decide.FailedTurn), "" if it did not.
	Afresh string
	// Waited is how long baton waited already before bringing it back: the loop's backoff before a fresh
	// session, or the host's own past the cap.
	Waited time.Duration
	// Started: a hook of this host's claude ran, so it reached its SessionStart (recoverLead reads it from
	// the run: its last activity came after the host started).
	Started bool
	// Recovering is this host's BATON_RECOVER: it was started to bring the lead back ("" if not).
	Recovering string
	CanExec    bool // this platform can exec a new host in place (upgrade.CanRestart)
}

// recovery is how the host brings the lead back.
type recovery struct {
	Why   string // what happened to the claude brought back: "claude exited with code 137"
	Fresh bool   // a fresh session from the phase's brief, since the conversation could not be resumed
	// Afresh: a fresh session, since an API error refused the conversation itself (leadExit.Afresh).
	Afresh bool
	// Quiet: the conversation is resumed with its model left asleep, as its last turn stands refused.
	Quiet bool
	// Wait is how long the host waits before it brings the lead back (state.Backoff), and Waited how long
	// baton waited already.
	Wait, Waited time.Duration
}

// exitWhy is what happened to a claude that exited by itself with code.
func exitWhy(code int) string { return fmt.Sprintf("claude exited with code %d", code) }

// recoverable decides whether the host brings the lead back after its claude ended (x), in the run whose
// state st is, which the host drives as instance, and how long it waits first (Wait, past the cap; t has
// the backoff). It does when claude exited by itself mid-plan in a way nobody chose: not ended by baton (a
// restart, the human leaving baton), by a signal or a hang-up; not with 0, 130 or 143 (a deliberate exit,
// Ctrl-C, a termination); not after a deliberate SessionEnd; and only while the run runs. It resumes the
// conversation of a claude that started in this host (quietly, when its last turn stands refused), and
// starts a fresh session when the claude of a host started to resume it exited before its session
// started (not for a refused one). It also does when the loop ended claude for a fresh session (Afresh),
// whose wait the loop waited.
func recoverable(st state.State, x leadExit, instance string, now time.Time, t loop.Timing) (r recovery, ok bool) {
	if x.Afresh != "" {
		if !x.CanExec || !st.Run.ExitAt.IsZero() || !st.IsOwner(instance, now) {
			return recovery{}, false
		}
		return recovery{Why: x.Afresh, Fresh: true, Afresh: true, Waited: x.Waited}, true
	}
	switch {
	case !x.CanExec, x.By != host.EndedSelf, x.Restart, x.Code == 0 || x.Code == 130 || x.Code == 143:
		return recovery{}, false
	case st.Mode != state.ModeRunning, st.Run.Ended.Deliberate(), !st.Run.ExitAt.IsZero(), !st.IsOwner(instance, now):
		return recovery{}, false
	case x.Started && st.Run.SessionID == "":
		return recovery{}, false // nothing to resume
	case !x.Started && x.Recovering == "":
		return recovery{}, false // this host's claude never started: there is nothing of its to bring back
	case !x.Started && st.RefusedNow():
		return recovery{}, false // no fresh conversation replaces a refused one: the human decides
	}
	r = recovery{Why: exitWhy(x.Code), Fresh: !x.Started, Quiet: x.Started && st.RefusedNow(), Waited: x.Waited}
	if r.Fresh {
		r.Why = strings.TrimPrefix(x.Recovering, freshPrefix) // what ended the conversation it could not resume
	}
	if x.Waited == 0 {
		r.Wait = state.Backoff(st.Run.Recoveries, now, t.RecoverAfter, t.RecoverEvery)
	}
	return r, true
}

// noteRecovery records in the run that the host brings the lead back (r) at now, forgetting the
// recoveries old enough not to count, and leaves line for the session it brings back to show.
func noteRecovery(st *state.State, r recovery, now time.Time, line string) {
	st.Run.Recoveries = append(state.Recent(st.Run.Recoveries, now), state.Recovery{At: now, Why: r.Why, Fresh: r.Fresh, Waited: r.Waited, Quiet: r.Quiet})
	// The session did not end for good: a notice its SessionEnd queued would say it had.
	st.Run.Notices = slices.DeleteFunc(st.Run.Notices, func(n state.Notice) bool { return n.Kind == "session_ended" })
	st.Run.Announce = append(st.Run.Announce, line)
}

// pause waits d, unless the human stops it with Ctrl-C, or the terminal goes away (SIGHUP, SIGTERM): it
// reports whether it waited all of d. It reads the wall clock, so that a machine that sleeps meanwhile
// does not stretch it. A variable, so that a test need not wait.
var pause = func(d time.Duration) bool {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for until := time.Now().Add(d).Round(0); time.Now().Round(0).Before(until); {
		select {
		case <-sig:
			return false
		case <-tick.C:
		}
	}
	return true
}

// holdOff waits r.Wait before the host brings the lead back, as claude has exited by itself more often in
// state.RecoveryWindow than it is brought back at once (st, the run as the host found it, at now): the
// human is pushed a notice saying when, and baton's terminal says so too. The phase agents ended with
// claude, so their workers are suspended now, for the next host to take up again (their jobs go on, as
// across any recovery), and nothing this host does after the wait touches them. The run stays the host's
// meanwhile, its heartbeat let go stale: a host the human starts for the same conversation takes it over,
// and this one then lets go (recoverable). It reports whether it waited: not when the human stopped it
// (Ctrl-C) or its terminal went away.
func holdOff(io IO, s *state.Store, st state.State, r recovery, now time.Time, t loop.Timing,
	coordinator *loop.Coordinator, controller *loop.Loop, logf func(string, ...any)) bool {
	until := now.Add(r.Wait)
	times := len(state.Recent(st.Run.Recoveries, now)) + 1
	text := decide.RecoveryBackoff(times, state.RecoveryWindow, r.Why, until, t.RecoverEvery, st.Run.SessionID)
	s.Update(func(st *state.State) error {
		// The session did not end for good: a notice its SessionEnd queued would say it had.
		st.Run.Notices = slices.DeleteFunc(st.Run.Notices, func(n state.Notice) bool { return n.Kind == "session_ended" })
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "recovery_backoff", Text: text, At: now})
		return nil
	})
	s.Event("recovery_backoff", map[string]any{"why": r.Why, "wait": r.Wait.String(), "until": until.UTC().Format(time.RFC3339), "times": times, "fresh": r.Fresh})
	coordinator.Ended("the lead's "+r.Why, nil)
	controller.Flush()
	fmt.Fprintf(io.Out, "baton: %s mid-plan, %d times in %s; baton brings it back at %s (Ctrl-C stops it here)…\n",
		r.Why, times, decide.Spell(state.RecoveryWindow), until.Local().Format("15:04"))
	logf("host: %s, %d times in %s; bringing the lead back at %s", r.Why, times, state.RecoveryWindow, until.Format(time.RFC3339))
	if !pause(r.Wait) {
		logf("host: stopped waiting to bring the lead back")
		return false
	}
	return true
}

// recoverArgv is the command line of the host that brings the lead back: the human's own claude
// arguments, with the session chosen anew. It resumes session, or starts a fresh session under that id
// with prompt as its first prompt.
func recoverArgv(args []string, session string, fresh bool, prompt string) []string {
	argv := append([]string{"baton"}, elevate.KeepUserArgs(args)...)
	if !fresh {
		return append(argv, "--resume", session)
	}
	return append(argv, "--session-id", session, "--", prompt)
}

// recoverEnv is the environment of the host that brings the lead back: this one's, with BATON_RECOVER
// saying why (freshPrefix first for a fresh session).
func recoverEnv(env []string, r recovery) []string {
	v := r.Why
	if r.Fresh {
		v = freshPrefix + r.Why
	}
	return append(withoutVar(env, "BATON_RECOVER"), "BATON_RECOVER="+v)
}

// recoverLead brings the lead back after its claude ended (x; started is when this host started it), if
// it should (recoverable), after the wait its recoveries owe (holdOff): it records the recovery in the
// run, ends what the host runs as a restart does (the workers are suspended, for the next host to resume,
// and jobs go on), and execs a new host on this binary, exe, with the human's claude arguments (args).
// It returns only when it does not: with handled false and, when the human stopped it bringing the lead
// back, the escalation (stop); or with handled true and the host's exit code, when the exec failed.
func recoverLead(io IO, proj *state.Project, instance, exe string, args []string, x leadExit, started time.Time, t loop.Timing,
	coordinator *loop.Coordinator, controller *loop.Loop, logf func(string, ...any)) (code int, handled bool, stop string) {
	s, ok := proj.Lookup("", instance)
	if !ok {
		return 0, false, ""
	}
	st, err := s.Load()
	if err != nil {
		return 0, false, ""
	}
	now := io.Now()
	x.Started = st.Run.LastActivity.After(started)
	r, ok := recoverable(st, x, instance, now, t)
	if !ok {
		return 0, false, ""
	}
	if r.Wait > 0 {
		if !holdOff(io, s, st, r, now, t, coordinator, controller, logf) {
			s.Event("lead_exited", map[string]any{"code": x.Code, "why": r.Why, "then": "stop", "by": "human"})
			return 0, false, decide.RecoveryStopped(st.Run.SessionID)
		}
		// Much may have changed meanwhile: the human may have resumed the conversation in another terminal.
		x.Waited = r.Wait
		if st, err = s.Load(); err != nil {
			return 0, false, ""
		}
		now = io.Now()
		if r, ok = recoverable(st, x, instance, now, t); !ok {
			return 0, false, ""
		}
	}
	resumed := st.Run.SessionID
	session, prompt, then := resumed, "", "resume"
	phase := ""
	if st.InHand() {
		phase = st.Current
	}
	if r.Fresh {
		session, then = state.NewSessionID(), "fresh"
		if err := proj.Link(session, s.Ref); err != nil {
			logf("host: cannot link a fresh session to run %s: %v", s.Ref, err)
			return 0, false, ""
		}
		prompt = words(io, s).Recovered(phase, r.Why, true)
		if r.Afresh {
			prompt = words(io, s).Afresh(phase, r.Why)
		}
	}
	var line string
	switch {
	case r.Afresh:
		line = decide.AfreshLine(r.Why, phase)
	case r.Fresh:
		from := "for the run"
		if phase != "" {
			from = "from " + phase + "'s brief"
		}
		line = fmt.Sprintf("baton: %s mid-plan, and the conversation could not be resumed → started a fresh session %s", r.Why, from)
	case r.Quiet:
		line = decide.ResumedQuietly(r.Why)
	default:
		line = fmt.Sprintf("baton: %s mid-plan → resumed this conversation", r.Why)
	}
	s.Update(func(st *state.State) error { noteRecovery(st, r, now, line); return nil })
	fields := map[string]any{"code": x.Code, "why": r.Why, "then": then, "session": session}
	for k, on := range map[string]bool{"afresh": r.Afresh, "quiet": r.Quiet} {
		if on {
			fields[k] = true
		}
	}
	if r.Waited > 0 {
		fields["waited"] = r.Waited.String()
	}
	s.Event("lead_exited", fields)
	// The phase agents ended with the lead's claude; their jobs go on, as the lead's do: the next host
	// watches them, and starts the agents again.
	ended := "the lead's " + exitWhy(x.Code)
	if r.Afresh {
		ended = "the lead started afresh"
	}
	coordinator.Ended(ended, nil)
	host.Release(proj, instance)
	controller.Flush()
	switch {
	case r.Afresh:
		fmt.Fprintf(io.Out, "baton: %s, which a fresh session avoids; starting one for this run…\n", r.Why)
	case r.Fresh:
		fmt.Fprintf(io.Out, "baton: the conversation could not be resumed; starting a fresh session for this run…\n")
	default:
		fmt.Fprintf(io.Out, "baton: %s mid-plan; resuming the conversation…\n", r.Why)
	}
	argv := recoverArgv(args, session, r.Fresh, prompt)
	logf("host: bringing the lead back (%s): %s %q", r.Why, exe, argv)
	err = upgrade.Exec(exe, argv, recoverEnv(os.Environ(), r))
	logf("host: could not bring the lead back: %v", err)
	return fail(io, "could not bring claude back (%v); resume the session with: baton --resume %s", err, resumed), true, ""
}
