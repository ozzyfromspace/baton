package cli

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The confirmation run (v0.8.15, state/confirm.go): at the done of a QA phase whose gate reruns cleared, or a
// rerun runs to clear, baton reads the files changed since the full run the reruns go back to, and the tests
// that failed since, before the run's lock (askConfirm). Files beyond Markdown and the failing tests' own
// mean one whole run of the gate's command, at HEAD, queued by baton itself under the lock (queue): now, or
// once the rerun passes (state.Settle). With none, done goes on as before.

// confirmAsk is the confirmation a QA phase owes at its done, as askConfirm found it before the run's lock.
type confirmAsk struct {
	asked   bool           // the phase is a QA phase whose gate is a rerun that passed, or runs on
	gate    state.Job      // that rerun
	of      string         // the full run the reruns go back to ("" when it is gone from the record)
	c       *state.Confirm // the run it owes; nil for none
	changed int            // how many files changed since the full run, when it owes none
	queued  *state.Job     // the confirmation, queued at the done (queue)
	stands  bool           // c was recorded under the lock (queue)
	// wrapping: the human is wrapping the run up (wrap.go), so it owes none
	wrapping bool
}

// confirmWait is how long askConfirm waits for git to list the files changed since the full run.
const confirmWait = 10 * time.Second

// askConfirm works out the confirmation phase owes at its done, from st and pl read before the run's lock:
// git and the jobs' logs are read here.
func askConfirm(s *state.Store, st state.State, pl plan.Plan, phase string) confirmAsk {
	var ca confirmAsk
	if !slices.ContainsFunc(pl.Phases, func(ph plan.Phase) bool { return ph.ID == phase && ph.QA() }) || !gitx.Usable(s.Root) {
		return ca
	}
	g, whole, ok := st.ConfirmFrom(phase)
	c := state.Confirm{Of: whole.ID, Command: whole.Command, Here: whole.Here, Commit: gitx.Head(s.Root)}
	if !ok || c.Command == "" || c.Commit == "" {
		return ca // no rerun cleared it, or nothing baton could run whole
	}
	ca.asked, ca.gate, ca.of = true, g, whole.ID
	if ca.wrapping = s.LeadWrap() != nil; ca.wrapping {
		return ca
	}
	var changed []string
	if c.Unknown = whole.ID == "" || whole.Commit == ""; !c.Unknown {
		ctx, cancel := context.WithTimeout(context.Background(), confirmWait)
		defer cancel()
		var err error
		changed, err = gitx.ChangedBetween(ctx, s.Root, whole.Commit, c.Commit)
		c.Unknown = err != nil
	}
	if !c.Unknown {
		files := state.NeedsConfirm(changed, failedSince(s, st, phase, whole))
		if len(files) == 0 {
			ca.changed = len(changed)
			return ca
		}
		c.Files = files
		if len(files) > state.MaxChanged {
			c.Files, c.More = files[:state.MaxChanged], len(files)-state.MaxChanged
		}
	}
	c.Notes = decide.ConfirmNotes(phase, c, g.Notes)
	ca.c = &c
	return ca
}

// failedSince are the tests phase's gates say failed from its full run, whole, on (from the start of the
// record when whole is gone from it): read from the logs of those that were red or showed a failure
// (failuresIn), and from the failures their stages recorded.
func failedSince(s *state.Store, st state.State, phase string, whole state.Job) []lane.FailedTest {
	from := max(0, slices.IndexFunc(st.Jobs, func(j state.Job) bool { return j.ID == whole.ID }))
	var jobs []state.Job
	for _, j := range st.Jobs[from:] {
		if j.Phase == phase && j.Kind != state.JobAwait && (j.Red() || j.Failing != nil) {
			jobs = append(jobs, j)
		}
	}
	fs, _ := failuresIn(s, jobs)
	for _, j := range jobs {
		for _, sg := range s.StagesOf(j) {
			for _, id := range sg.Failures {
				if f := (lane.FailedTest{Runner: sg.Runner, ID: id}); !slices.Contains(fs, f) {
					fs = append(fs, f)
				}
			}
		}
	}
	return fs
}

// queue records, under the run's lock, the confirmation phase owes (state.ConfirmAtDone), before its done
// is recorded: queued now, the phase then verifies on it, or owed once the rerun it verifies on passes. The
// human is told. A phase that owes none has none on record.
func (ca *confirmAsk) queue(st *state.State, phase string, now time.Time) {
	if !ca.asked {
		return
	}
	if ps := st.Phases[phase]; ps != nil {
		ps.Confirm = nil
	}
	if ca.c == nil {
		return
	}
	q, ok := state.ConfirmAtDone(st, phase, *ca.c, ca.gate.ID, now)
	if ca.stands = ok; !ok {
		return
	}
	c := *st.Phases[phase].Confirm
	ca.c = &c
	if q.ID != "" {
		ca.queued = &q
	}
	st.Run.Announce = append(st.Run.Announce, "baton: "+decide.ConfirmQueued(phase, c))
}

// report logs what done decided of phase's confirmation and tells the model, once done is recorded; the
// human in the lead's session too, for a phase agent's.
func (ca confirmAsk) report(io IO, s *state.Store, phase string) {
	switch {
	case !ca.asked:
	case ca.c == nil && ca.wrapping:
		ca.wrapSkipped(io, s, phase)
	case ca.c == nil:
		s.Event("qa_confirm_skipped", map[string]any{"phase": phase, "of": ca.of, "gate": ca.gate.ID, "changed": ca.changed})
		fmt.Fprintln(io.Out, "baton: "+decide.ConfirmSkipped(phase, ca.of, ca.changed))
	case !ca.stands:
	default:
		ev, kind, say := ca.c.Fields(), "qa_confirm_owed", decide.ConfirmOwed(phase, *ca.c)
		if ca.queued != nil {
			kind, say = "qa_confirm_queued", decide.Confirming(phase, *ca.c)
		}
		ev["phase"], ev["by"] = phase, "done"
		s.Event(kind, ev)
		tellLead(s, decide.ConfirmQueued(phase, *ca.c))
		fmt.Fprintln(io.Out, "baton: "+say)
	}
}
