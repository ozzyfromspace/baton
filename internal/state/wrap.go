package state

import (
	"fmt"
	"slices"
	"time"
)

// The human wraps a run up (v0.8.17, `/baton wrap`): every phase still runs and lands, but no gate runs whole a
// second time. A real run's final QA phase ran its 40–49 minute set whole four times, and its human, who wanted
// the run over, had nothing lighter to say so with than dropping it. While the lead's run is wrapping up
// (Wrap), a gate whose command already ran whole in its phase is refused (WholeRepeat), confirmation runs are
// owed no more, and those under way, with every whole repeat under way, are stopped: each phase finishes on the
// evidence it has, rerunning only what failed. A phase's first run of its gate keeps going, since it is that
// phase's only evidence. Wrap lives on State, not Runtime, so a new plan starts without it, as it starts
// without everything else of the plan before (attachKeeping).

// MessageWrap tells a model that the human is wrapping the run up (Wrap): it finishes on the evidence it has.
const MessageWrap = "wrap"

// WrapByHuman is who wraps a run up (Wrap.By): the human, by /baton wrap in the lead's session, the only way.
const WrapByHuman = "human"

// Wrap is the human wrapping the run up (State.Wrap, in the lead's run alone): since At, by By.
type Wrap struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
}

// LeadWrap is the Wrap of the lead's run: s's own, or for a phase agent's sub-run the run it works for, read
// without its lock (Peek), as its gates and briefs read it before their own run's lock. nil when the run is
// not wrapping up, or the lead's run cannot be read.
func (s *Store) LeadWrap() *Wrap {
	lead := s
	if s.Parent != "" {
		up, err := s.ParentRun()
		if err != nil {
			return nil
		}
		lead = up
	}
	st, err := lead.Peek()
	if err != nil {
		return nil
	}
	return st.Wrap
}

// ranWhole reports a gate that ran its command whole: no rerun of what failed (Reruns) nor a narrowed one
// (Narrows), with a verdict, or stopped after it showed a failure (a gate stopped at its first failure, which
// `baton verify --failed` reruns). One stopped before it showed anything, or that could not run, checked
// nothing.
func ranWhole(j Job) bool {
	return j.Reruns == "" && len(j.Narrows) == 0 &&
		(j.Status == JobPassed || j.Red() || j.Status == JobInterrupted && j.Failing != nil)
}

// WholeRepeat is the gate that j, a verify job queued or about to be (no ID), repeats whole: phase j.Phase's
// latest gate before it that ran j's command whole (ranWhole), in this plan's record. nil when j is no whole
// repeat: an await, a rerun of what failed (Reruns, whole stages and all), a narrowed rerun (Narrows), or its
// command's first whole run in its phase, which is that phase's evidence.
func (st State) WholeRepeat(j Job) *Job {
	if j.kind() != JobVerify || j.Reruns != "" || len(j.Narrows) > 0 {
		return nil
	}
	var w *Job
	for i := range st.Jobs {
		x := &st.Jobs[i]
		if j.ID != "" && x.ID == j.ID {
			break
		}
		if x.Phase == j.Phase && x.kind() == JobVerify && x.Command == j.Command && ranWhole(*x) {
			w = x
		}
	}
	return w
}

// DropConfirm drops the confirmation run phase owes once the rerun it verifies on passes (Confirm.After, not
// queued yet), as the human wraps the run up: the phase verifies on that rerun alone, and Settle finds it done
// once it passes. It returns the confirmation dropped, and false when phase owes none.
func DropConfirm(st *State, phase string) (Confirm, bool) {
	ps := st.Phases[phase]
	if ps == nil || ps.Confirm == nil || ps.Confirm.Job != "" || ps.Confirm.After == "" {
		return Confirm{}, false
	}
	c := *ps.Confirm
	ps.Confirm = nil
	return c, true
}

// StopConfirm stops phase's confirmation run under way (Confirm.Job), for reason, as the human wraps the run
// up (StopJob), and settles the phase on the rerun that passed before it (rerun, "" when it is gone from the
// record): the phase verifies on that rerun in the confirmation's place (PhaseState.On), and Settle then finds
// it done. The confirmation is owed no more. It returns the run stopped as it was, and false when phase has
// none under way.
func StopConfirm(st *State, phase, reason string, now time.Time) (stopped Job, rerun string, ok bool) {
	ps := st.Phases[phase]
	if ps == nil || ps.Confirm == nil || ps.Confirm.Job == "" {
		return Job{}, "", false
	}
	conf := st.Job(ps.Confirm.Job)
	if conf == nil || conf.Finished() {
		return Job{}, "", false
	}
	rerun = st.passedRerunBefore(phase, conf.ID)
	if stopped, ok = StopJob(st, conf.ID, reason, now); !ok {
		return Job{}, "", false
	}
	on := slices.DeleteFunc(slices.Clone(ps.On), func(id string) bool { return id == stopped.ID || id == rerun })
	if rerun != "" {
		on = append([]string{rerun}, on...)
	}
	ps.On, ps.Confirm = on, nil
	return stopped, rerun, true
}

// passedRerunBefore is phase's latest rerun of what failed (Job.Reruns) queued before job id that passed: what
// a confirmation of its reruns follows. "" for none on record.
func (st State) passedRerunBefore(phase, id string) string {
	rerun := ""
	for _, j := range st.Jobs {
		if j.ID == id {
			break
		}
		if j.Phase == phase && j.kind() == JobVerify && j.Reruns != "" && j.Status == JobPassed {
			rerun = j.ID
		}
	}
	return rerun
}

// WrapNote records, on phase, what wrapping the run up did to it, with its undo: a decision the human made by
// wrapping up, so it is listed with the run's decisions, shown in words of its own (Announced) and counted
// toward no review (Reviewed). It is recorded in any mode a plan runs in, paused too, as the human chose it.
func WrapNote(st *State, phase, what, undo string, now time.Time) Decision {
	d := Decision{ID: fmt.Sprintf("%sd%d", st.IDPrefix, len(st.Decisions)+1), Kind: Note, Phase: phase, What: what, Undo: undo, At: now,
		Announced: true, Reviewed: true}
	st.Decisions = append(st.Decisions, d)
	return d
}
