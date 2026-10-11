package loop

import (
	"fmt"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// switching moves an idle session off a phase that waits on its job, and calls off a switch the job beat.
//
// Once the job has run for SwitchAfter (sooner for a command known to take that long: state.SwitchAfter)
// and the session sits idle on it, the phase is set aside for the next phase that can go on
// (state.Switch), and the compaction that queues is typed like any other. Never with the phase's work
// uncommitted: it would sit in the checkout while another phase works there, and the phase, resumed,
// would take it for work it found. A switch whose job ends before its compaction is typed is called off
// (state.Unswitch), and the result is given in place. It returns the state as it left it.
func (l *Loop) switching(v host.View, st state.State) state.State {
	if sw := st.Run.Compaction.Switch; sw != nil && st.Run.Compaction.Status == state.CompactQueued {
		if j := st.Job(sw.Job); j == nil || j.Finished() {
			return l.unswitch(st)
		}
		return st
	}
	j := st.CurrentJob()
	// The wait is measured on the wall clock: the job's runner outlives a restarted host.
	if j == nil || !l.idle(v, st) || !state.Switchable(st) || v.Now.Sub(j.Queued) < state.SwitchAfter(st, *j, l.Timing.SwitchAfter) {
		return st
	}
	if v.Now.Before(l.switchNext) {
		return st // each try reads the plan, and the checkout's state
	}
	l.switchNext = v.Now.Add(settleEvery)
	pl, err := l.Store.LoadPlan()
	if err != nil {
		l.logf("loop: switching: %v", err)
		return st
	}
	to := state.Next(st, pl)
	if to == "" {
		return st
	}
	if w := l.Store.PhaseWork(st.Current); !w.Clean() {
		l.switchNext = v.Now.Add(switchRecheck)
		if l.switchHeld != j.ID {
			l.switchHeld = j.ID
			l.logf("loop: %s waits on job %s with its work uncommitted; it is not set aside", st.Current, j.ID)
			files := w.Fresh
			if len(files) == 0 {
				files = w.Paths // baton cannot tell the phase's own from the rest
			}
			l.Store.Event("switch_held", map[string]any{"phase": st.Current, "job": j.ID, "why": "uncommitted work", "files": len(files)})
			unit := "files"
			if len(files) == 1 {
				unit = "file"
			}
			line := fmt.Sprintf("baton: %s's job %s has run %s, but %s has %d uncommitted %s, so baton keeps it in hand rather than set it aside for %s",
				st.Current, j.ID, decide.Span(v.Now.Sub(j.Queued)), st.Current, len(files), unit, to)
			l.update(func(st *state.State) { st.Run.Announce = append(st.Run.Announce, line) })
		}
		return st
	}
	var sw state.Switched
	var ok bool
	var epoch int
	nst, err := l.Store.Update(func(st *state.State) error {
		if !l.idle(v, *st) {
			return nil
		}
		if sw, ok = state.Switch(st, pl, v.Now); ok {
			epoch = st.Run.Compaction.Epoch
			st.Run.Announce = append(st.Run.Announce, switchLine(*st, sw))
		}
		return nil
	})
	if err != nil {
		l.logf("loop: switching: %v", err)
		return st
	}
	if !ok {
		return nst
	}
	waited := decide.Span(v.Now.Sub(j.Queued))
	l.logf("loop: %s waits on job %s (%s so far): switching to %s", sw.From, sw.Job, waited, sw.To)
	l.Store.Event("phase_parked", map[string]any{"phase": sw.From, "reason": state.ParkJob, "job": sw.Job, "status": j.Status})
	l.Store.Event("phase_switched", map[string]any{"from": sw.From, "to": sw.To, "job": sw.Job, "waited": waited})
	l.Store.Event("compact_queued", map[string]any{"reason": "switch", "epoch": epoch, "next": sw.To, "by": "switch"})
	return nst
}

// switchRecheck is how soon a switch held by uncommitted work is looked at again.
const switchRecheck = 30 * time.Second

// idle reports a session sitting idle: no turn under way (one that shows no sign of life for StaleTurn is
// not trusted to be), and nothing on screen waiting for the human.
func (l *Loop) idle(v host.View, st state.State) bool {
	r := st.Run
	open := r.TurnOpen && v.Now.Sub(latest(r.LastActivity, r.TurnStarted, l.wokeAt)) < l.Timing.StaleTurn
	return !open && !r.Dialogs.AnyOpen()
}

// unswitch calls off a switch whose job ended before its compaction was typed. The human was to be told
// of the switch with the next hook; if they have not been yet, they never are.
func (l *Loop) unswitch(st state.State) state.State {
	var sw state.Switched
	var ok bool
	var status string
	nst, err := l.Store.Update(func(st *state.State) error {
		line := ""
		if x := st.Run.Compaction.Switch; x != nil {
			line = switchLine(*st, *x)
		}
		if sw, ok = state.Unswitch(st); !ok {
			return nil
		}
		if j := st.Job(sw.Job); j != nil {
			status = j.Status
		}
		if i := slices.Index(st.Run.Announce, line); i >= 0 {
			st.Run.Announce = slices.Delete(st.Run.Announce, i, i+1)
		} else {
			st.Run.Announce = append(st.Run.Announce, fmt.Sprintf("baton: job %s ended before the switch → %s goes on, and %s waits", sw.Job, sw.From, sw.To))
		}
		return nil
	})
	if err != nil {
		l.logf("loop: calling off the switch: %v", err)
		return st
	}
	if ok {
		l.Store.Event("switch_cancelled", map[string]any{"from": sw.From, "to": sw.To, "job": sw.Job, "status": status})
	}
	return nst
}

// switchLine tells the human about a switch.
func switchLine(st state.State, sw state.Switched) string {
	cmd, verb := "", "starts"
	if j := st.Job(sw.Job); j != nil {
		cmd = " (`" + j.Command + "`)"
	}
	if ps := st.Phases[sw.To]; ps != nil && ps.Status == state.PhaseParked {
		verb = "resumes"
	}
	return fmt.Sprintf("baton: %s waits on job %s%s → set aside; compacting, then %s %s, and %s resumes once the job ends", sw.From, sw.Job, cmd, sw.To, verb, sw.From)
}
