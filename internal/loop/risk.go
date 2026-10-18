package loop

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/risk"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The risk ledger (package risk, state.Risk), kept by the coordinator. Each phase is scored once it is done
// (a lead phase) or has landed (an agent's), from facts baton has of it: git is read before the run's lock is
// taken, and the points go to the run's ledger (risk_scored). A passing full check starts the ledger again
// (risk_reset): a QA phase done, unless over a red verification, or a --qa gate of the lead's that passed;
// either takes out the phases whose work it checked, those done before the check began.
//
// When the total crosses a line (Coordinator.Risk: risk_review, then risk_full), the coordinator hands over
// (handOver, risk_crossed): an independent model assesses the exposure (assess.go), and the lead is told what
// it says; with no assessment to ask, or none answering, the lead is told the score and its factors as a
// look-in, and nothing is held. A look-in waits for the lead to hold a phase: with none in hand it could
// queue no check.
//
// Only an assessment's "full now" verdict (riskVerdict) holds anything (risk_held): no new phase agent starts
// (Fleet.next's start gate, worker_deferred), and the lead is told why, as soon as it holds a phase (at its
// next tool call or stop, and first thing in a phase it takes up meanwhile: its own next phase starts with
// the check): queue the full check with `baton verify --qa`, in a lane rather than --here (the landings a
// check in the checkout would hold are named), or `baton note` why not. Either lifts the hold, and work goes
// on beside the check; so does escalation_timeout from the verdict, and a full check that passes meanwhile
// (risk_released{by}).

// riskEvery is how often the ledger's step may run.
const riskEvery = 2 * time.Second

// riskBatch is how many phases a step scores at most: a run carried onto a baton with the ledger has every
// phase it did before to score once.
const riskBatch = 8

// ledger is the ledger step's own state. Only the lead's tick touches at.
type ledger struct {
	busy atomic.Bool
	at   time.Time
	// The assessment's (assess.go): the crossing whose call runs in this host (its Risk.Asking, in Unix ns;
	// 0: none), the calls under way, and how a call is made (tests stub it; nil: Complete).
	asking atomic.Int64
	calls  sync.WaitGroup
	call   func(ctx context.Context, s *state.Store, c Completion) (Answer, error)
}

// riskLedger starts the ledger's step, off the lead's tick, when it has something to do (riskDue). A paused
// run scores nothing: the human has the wheel.
func (c *Coordinator) riskLedger(v host.View, s *state.Store, st state.State) {
	if !c.Risk.On || c.Project == nil || s.Worker() != "" || st.Mode != state.ModeRunning && st.Mode != state.ModeComplete {
		return
	}
	if v.Now.Sub(c.rsk.at) < riskEvery {
		return
	}
	c.rsk.at = v.Now
	pl, err := s.LoadPlan()
	if err != nil || !riskDue(st, pl) || !c.rsk.busy.CompareAndSwap(false, true) {
		return
	}
	c.fleetWG.Add(1)
	run := func(fn func()) { go fn() }
	if c.Fleet != nil {
		run = c.Fleet.run
	}
	run(func() {
		defer c.fleetWG.Done()
		defer c.rsk.busy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				c.Lead.logf("loop: risk: panic: %v", r)
				s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "risk"})
			}
		}()
		c.riskStep(v.Now, s)
	})
}

// riskDue reports something for the ledger's step: a phase done and not scored, a full check passed that has
// not reset it, a look-in owed to a lead that holds a phase, or a hold to tell of or lift.
func riskDue(st state.State, pl plan.Plan) bool {
	if len(st.Unscored(pl)) > 0 || len(passedFull(st)) > 0 {
		return true
	}
	r := st.Risk
	return r != nil && (r.Owed != "" && st.InHand() || !r.Held.IsZero() || !r.Asking.IsZero())
}

// passedFull lists the lead's full checks (--qa gates) that passed and have not reset the ledger.
func passedFull(st state.State) []state.Job {
	var out []state.Job
	for _, j := range st.Jobs {
		if j.QA != "" && j.Kind == state.JobVerify && j.Status == state.JobPassed && !st.Cleared(j.ID) {
			out = append(out, j)
		}
	}
	return out
}

// riskStep is one step of the ledger: score the phases done since, reset it on the full checks that
// passed, hand over at a line crossed, and tell of, or lift, a hold.
func (c *Coordinator) riskStep(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil {
		return
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return
	}
	var doc []byte
	if pl.File != "" {
		doc, _ = os.ReadFile(pl.File)
	}
	for i, id := range st.Unscored(pl) {
		if i == riskBatch {
			break
		}
		c.score(now, s, st, pl, doc, id)
	}
	if st, err = s.Load(); err != nil {
		return
	}
	for _, j := range passedFull(st) {
		c.riskReset(now, s, "job", j.ID, j.ID, j.Ended, j.Commit, j.Queued)
	}
	c.riskCrossing(now, s)
	c.riskAsking(now, s)
	c.riskHold(now, s)
	c.riskOwed(now, s)
}

// score scores phase id of run st, done, once: a QA phase done (not over a red verification) resets the
// ledger instead, its full check having passed.
func (c *Coordinator) score(now time.Time, s *state.Store, st state.State, pl plan.Plan, doc []byte, id string) {
	i := pl.Index(id)
	ps := st.Phases[id]
	if i < 0 || ps == nil {
		return
	}
	rec := risk.Record{Phase: id, Plan: pl, Doc: doc, Lead: st, Root: s.Checkout}
	if ps.Worker != "" {
		if sub, err := c.Project.ExistingRun(ps.Worker, ""); err == nil {
			if sst, err := sub.Load(); err == nil {
				rec.Sub = &sst
			}
		}
	}
	at, commit := ps.DoneAt, ""
	if l := landingOf(st, id); l != nil {
		at, commit = l.At, l.Head
	}
	own := st
	if rec.Sub != nil {
		own = *rec.Sub
	}
	if pl.Phases[i].QA() && !own.FinishedDespiteRed(id) && !st.FinishedDespiteRed(id) {
		if j := own.LatestOf(id, state.JobVerify); j != nil && j.Status == state.JobPassed && j.Commit != "" {
			commit = j.Commit
		}
		covered := ps.StartedAt
		if covered.IsZero() {
			covered = at
		}
		s.Update(func(st *state.State) error { state.MarkScored(st, id); return nil })
		c.riskReset(now, s, "qa", id, "qa:"+id, at, commit, covered)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	facts := risk.Gather(ctx, rec)
	cancel()
	factors := risk.Score(facts, risk.DefaultWeights)
	pr := state.PhaseRisk{Phase: id, Points: risk.Points(factors), Factors: factors, Deviation: facts.Deviation, At: at}
	total, scored := 0, false
	if _, err := s.Update(func(st *state.State) error {
		if scored = !st.Scored(id); scored {
			total = state.AddRisk(st, pr)
		}
		return nil
	}); err != nil || !scored {
		return
	}
	s.Event("risk_scored", map[string]any{"phase": id, "points": pr.Points, "factors": factorLog(factors), "total": total, "git": facts.GitKnown})
}

// factorLog is factors as risk_scored logs them: name → points.
func factorLog(factors []state.RiskFactor) map[string]int {
	out := map[string]int{}
	for _, f := range factors {
		out[f.Name] = f.Points
	}
	return out
}

// riskReset starts the ledger again for a full check that passed (kind "qa": QA phase what done; "job": the
// --qa gate what), once (by), at at on commit, covering the phases done before covered. A hold standing
// goes with it.
func (c *Coordinator) riskReset(now time.Time, s *state.Store, kind, what, by string, at time.Time, commit string, covered time.Time) {
	var reset, held bool
	var total int
	s.Update(func(st *state.State) error {
		held = st.RiskHeld()
		reset = state.ResetRisk(st, by, at, commit, covered)
		total = st.RiskTotal()
		return nil
	})
	if !reset {
		return
	}
	fields := map[string]any{"by": kind, "total": total}
	if commit != "" {
		fields["commit"] = commit
	}
	if kind == "qa" {
		fields["phase"] = what
	} else {
		fields["job"] = what
	}
	s.Event("risk_reset", fields)
	if held {
		s.Event("risk_released", map[string]any{"by": decide.RiskByCleared, kind: what})
		c.announce(s, decide.RiskReleasedSay(decide.RiskByCleared, what))
	}
}

// riskCrossing hands over once the ledger's total crosses one of the plan's lines (risk.Lines.Crossed).
func (c *Coordinator) riskCrossing(now time.Time, s *state.Store) {
	if st, err := s.Load(); err != nil || st.Risk == nil || c.Risk.Crossed(st.Risk.Crossed, st.Risk.Total) == "" {
		return // read first: a step with nothing crossed takes no lock
	}
	var line string
	var r state.Risk
	s.Update(func(st *state.State) error {
		if st.Risk == nil {
			return nil
		}
		if line = c.Risk.Crossed(st.Risk.Crossed, st.Risk.Total); line != "" {
			st.Risk.Crossed, r = line, *st.Risk
		}
		return nil
	})
	if line != "" {
		c.handOver(now, s, r, line)
	}
}

// handOver is what crossing line does, with the ledger r: the score and its factors are kept as the lead's
// look-in, given once it holds a phase. With an assessment to ask (assess.go), the look-in waits for its
// answer, which takes its place, until the call's deadline; with none, it goes as it is, and nothing is
// held.
func (c *Coordinator) handOver(now time.Time, s *state.Store, r state.Risk, line string) {
	points := c.Risk.Line(line)
	lookIn := decide.RiskLookIn(r, line, points)
	why := c.assessable(s)
	if why == "" {
		c.rsk.asking.Store(now.UnixNano())
	}
	s.Update(func(st *state.State) error {
		if st.Risk != nil {
			st.Risk.Owed, st.Risk.Asking = lookIn, time.Time{}
			if why == "" {
				st.Risk.Asking = now
			}
		}
		return nil
	})
	s.Event("risk_crossed", map[string]any{"line": line, "points": points, "total": r.Total, "assessed": why == ""})
	if why != "" {
		s.Event("risk_assess_skipped", map[string]any{"why": why, "line": line})
		c.announce(s, decide.RiskCrossedSay(r.Total, line, points))
		return
	}
	c.announce(s, decide.RiskAssessingSay(r.Total, line, points, c.Risk.Model))
	c.assess(now, s, line, points)
}

// riskOwed gives the lead the look-in owed to it, once it holds a phase and no assessment is asked of it.
func (c *Coordinator) riskOwed(now time.Time, s *state.Store) {
	if st, err := s.Load(); err != nil || st.Risk == nil || st.Risk.Owed == "" || !st.Risk.Asking.IsZero() || !st.InHand() {
		return
	}
	told := false
	s.Update(func(st *state.State) error {
		if r := st.Risk; r != nil && r.Owed != "" && r.Asking.IsZero() && st.InHand() {
			state.Tell(st, state.Message{Kind: state.MessageRisk, Text: r.Owed, About: riskLookIn}, now)
			r.Owed, told = "", true
		}
		return nil
	})
	if told {
		s.Event("risk_told", map[string]any{"what": "look-in"})
	}
}

// What baton's messages about the ledger are about (state.Message.About): a newer one replaces one not
// given yet.
const (
	riskLookIn = "risk_look_in"
	riskHold   = "risk_hold"
)

// riskVerdict records an assessment's verdict of the ledger (state.RiskVerdict): "full now" holds new
// phases, told and logged here.
func (c *Coordinator) riskVerdict(now time.Time, s *state.Store, verdict string) {
	held, total := false, 0
	s.Update(func(st *state.State) error {
		held, total = state.RiskVerdict(st, verdict, now), st.RiskTotal()
		return nil
	})
	if !held {
		return
	}
	deadline := now.Add(c.Lead.Timing.EscalationTimeout)
	s.Event("risk_held", map[string]any{"verdict": verdict, "total": total, "until": deadline.UTC().Format(time.RFC3339)})
	c.announce(s, decide.RiskHeldSay(total, deadline))
}

// riskHold tells the lead of a hold on new phases once it holds a phase, and lifts the hold once the lead
// queued a full check (a --qa job) since the hold began, or noted why not since it was told, or
// escalation_timeout after the verdict.
func (c *Coordinator) riskHold(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil || !st.RiskHeld() {
		return
	}
	r := *st.Risk
	deadline := r.Held.Add(c.Lead.Timing.EscalationTimeout)
	by, what := "", ""
	switch j, d := fullQueued(st, r.Held), noted(st, r.Told); {
	case j != nil:
		by, what = decide.RiskByVerify, j.ID
	case d != nil:
		by, what = decide.RiskByNote, d.ID
	case !now.Before(deadline):
		by = decide.RiskByDeadline
	}
	if by != "" {
		released := false
		s.Update(func(st *state.State) error {
			if released = state.ReleaseRisk(st); released {
				st.Inbox = slices.DeleteFunc(st.Inbox, func(m state.Message) bool { return m.About == riskHold })
			}
			return nil
		})
		if released {
			fields := map[string]any{"by": by, "held": now.Sub(r.Held).Round(time.Second).Seconds()}
			switch by {
			case decide.RiskByVerify:
				fields["job"] = what
			case decide.RiskByNote:
				fields["decision"] = what
			}
			s.Event("risk_released", fields)
			c.announce(s, decide.RiskReleasedSay(by, what))
		}
		return
	}
	if !r.Told.IsZero() || !st.InHand() {
		return
	}
	text := decide.RiskHeld(r, st.LandingPhases(), workingPhases(st), deadline)
	told := false
	s.Update(func(st *state.State) error {
		if told = st.RiskHeld() && st.Risk.Told.IsZero() && st.InHand(); told {
			state.Tell(st, state.Message{Kind: state.MessageRisk, Text: text, About: riskHold}, now)
			st.Risk.Told = now
		}
		return nil
	})
	if told {
		s.Event("risk_told", map[string]any{"what": "hold", "phase": st.Current, "until": deadline.UTC().Format(time.RFC3339)})
	}
}

// fullQueued is the full check (a job with --qa) the lead queued since since, or nil.
func fullQueued(st state.State, since time.Time) *state.Job {
	for i := len(st.Jobs) - 1; i >= 0; i-- {
		if j := st.Jobs[i]; j.QA != "" && !j.Queued.Before(since) {
			return &st.Jobs[i]
		}
	}
	return nil
}

// noted is a note the lead made since it was told of the hold (told; none before it was told), or nil.
func noted(st state.State, told time.Time) *state.Decision {
	if told.IsZero() {
		return nil
	}
	for i := len(st.Decisions) - 1; i >= 0; i-- {
		if d := st.Decisions[i]; d.Kind == state.Note && !d.At.Before(told) {
			return &st.Decisions[i]
		}
	}
	return nil
}

// workingPhases are the phases of run st whose agents are at work: their work would wait too behind a check
// in the checkout.
func workingPhases(st state.State) []string {
	var out []string
	for _, w := range st.LiveWorkers() {
		if w.Status == state.WorkerWorking && w.Generation == st.Generation {
			out = append(out, w.Phase)
		}
	}
	return out
}
