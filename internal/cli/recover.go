package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
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
// starts a fresh session from the phase's brief, once per phase. The new host's session learns why from
// BATON_RECOVER, and the run's state (Runtime.Recoveries) says what to tell its model, once.

// The host brings the lead back at most maxRecoveries times in recoveryWindow; claude exiting again within
// it ends the run's session, as before, and the human is asked to resume it.
const (
	maxRecoveries  = 2
	recoveryWindow = 30 * time.Minute
)

// freshPrefix starts BATON_RECOVER for a fresh session: "fresh: claude exited with code 137".
const freshPrefix = "fresh: "

// leadExit is how the lead's claude ended, as the host saw it.
type leadExit struct {
	host.Exit
	Restart bool // the host ended it to restart the session on a newer baton
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
	// Stop is set when baton stops bringing the lead back: claude exited by itself once more than the cap
	// allows, or its phase has had its fresh session. It is the escalation, worded by decide.
	Stop string
}

// exitWhy is what happened to a claude that exited by itself with code.
func exitWhy(code int) string { return fmt.Sprintf("claude exited with code %d", code) }

// recoverable decides whether the host brings the lead back after its claude ended (x), in the run whose
// state st is, which the host drives as instance. It does when claude exited by itself mid-plan in a way
// nobody chose: not ended by baton (a restart, the human leaving baton), by a signal or a hang-up; not
// with 0, 130 or 143 (a deliberate exit, Ctrl-C, a termination); not after a deliberate SessionEnd; and
// only while the run runs. It resumes the conversation of a claude that started in this host, and starts a
// fresh session when the claude of a host started to resume it exited before its session started. ok is
// false with Stop set when baton would have, but stops: the cap, or a phase's fresh session spent.
func recoverable(st state.State, x leadExit, instance string, now time.Time) (r recovery, ok bool) {
	switch {
	case !x.CanExec, x.By != host.EndedSelf, x.Restart, x.Code == 0 || x.Code == 130 || x.Code == 143:
		return recovery{}, false
	case st.Mode != state.ModeRunning, st.Run.Ended.Deliberate(), !st.Run.ExitAt.IsZero(), !st.IsOwner(instance, now):
		return recovery{}, false
	case x.Started && st.Run.SessionID == "":
		return recovery{}, false // nothing to resume
	case !x.Started && x.Recovering == "":
		return recovery{}, false // this host's claude never started: there is nothing of its to bring back
	}
	why := exitWhy(x.Code)
	r = recovery{Why: why, Fresh: !x.Started}
	if r.Fresh {
		r.Why = strings.TrimPrefix(x.Recovering, freshPrefix) // what ended the conversation it could not resume
	}
	switch {
	case len(recent(st.Run.Recoveries, now)) >= maxRecoveries:
		r.Stop = decide.RecoveryCapped(maxRecoveries+1, recoveryWindow, st.Run.SessionID)
	case r.Fresh && st.Run.FreshFor != nil && *st.Run.FreshFor == st.Current:
		r.Stop = decide.FreshSpent(st.Current, why, st.Run.SessionID)
	}
	return r, r.Stop == ""
}

// recent is the recoveries within recoveryWindow of now: older ones are forgotten.
func recent(rs []state.Recovery, now time.Time) []state.Recovery {
	return slices.DeleteFunc(slices.Clone(rs), func(r state.Recovery) bool { return now.Sub(r.At) >= recoveryWindow })
}

// noteRecovery records in the run that the host brings the lead back (r) at now, forgetting the
// recoveries old enough not to count, and leaves line for the session it brings back to show.
func noteRecovery(st *state.State, r recovery, now time.Time, line string) {
	st.Run.Recoveries = append(recent(st.Run.Recoveries, now), state.Recovery{At: now, Why: r.Why, Fresh: r.Fresh})
	if r.Fresh {
		current := st.Current
		st.Run.FreshFor = &current
	}
	// The session did not end for good: a notice its SessionEnd queued would say it had.
	st.Run.Notices = slices.DeleteFunc(st.Run.Notices, func(n state.Notice) bool { return n.Kind == "session_ended" })
	st.Run.Announce = append(st.Run.Announce, line)
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
// it should (recoverable): it records the recovery in the run, ends what the host runs as a restart does
// (the workers are suspended, for the next host to resume, and jobs go on), and execs a new host on this
// binary, exe, with the human's claude arguments (args). It returns only when it does not: with handled
// false and, when baton stops bringing the lead back, the escalation (stop); or with handled true and the
// host's exit code, when the exec failed.
func recoverLead(io IO, proj *state.Project, instance, exe string, args []string, x leadExit, started time.Time,
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
	r, ok := recoverable(st, x, instance, now)
	if !ok {
		if r.Stop != "" {
			s.Event("lead_exited", map[string]any{"code": x.Code, "why": exitWhy(x.Code), "then": "stop"})
		}
		return 0, false, r.Stop
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
	}
	line := fmt.Sprintf("baton: %s mid-plan → resumed this conversation", r.Why)
	if r.Fresh {
		from := "for the run"
		if phase != "" {
			from = "from " + phase + "'s brief"
		}
		line = fmt.Sprintf("baton: %s mid-plan, and the conversation could not be resumed → started a fresh session %s", r.Why, from)
	}
	s.Update(func(st *state.State) error { noteRecovery(st, r, now, line); return nil })
	s.Event("lead_exited", map[string]any{"code": x.Code, "why": exitWhy(x.Code), "then": then, "session": session})
	// The phase agents ended with the lead's claude; their jobs go on, as the lead's do: the next host
	// watches them, and starts the agents again.
	coordinator.Ended("the lead's "+exitWhy(x.Code), nil)
	host.Release(proj, instance)
	controller.Flush()
	if r.Fresh {
		fmt.Fprintf(io.Out, "baton: the conversation could not be resumed; starting a fresh session for this run…\n")
	} else {
		fmt.Fprintf(io.Out, "baton: %s mid-plan; resuming the conversation…\n", r.Why)
	}
	argv := recoverArgv(args, session, r.Fresh, prompt)
	logf("host: bringing the lead back (%s): %s %q", r.Why, exe, argv)
	err = upgrade.Exec(exe, argv, recoverEnv(os.Environ(), r))
	logf("host: could not bring the lead back: %v", err)
	return fail(io, "could not bring claude back (%v); resume the session with: baton --resume %s", err, resumed), true, ""
}
