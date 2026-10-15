// Package board is baton's view of a run, in the lead's own terminal: the plan step by step, how each
// phase stands (the lead's, a phase agent's, verifying, landing, waiting on others or on the human), and
// what the lead session is doing. A step is a level of the plan's graph (plan.Steps): phases that may run
// at once. baton's mod draws the band above the prompt (Board.Band) and the /baton board pane
// (Board.Lines) from `baton board`, which gives the board whole as JSON too. It reads the run and changes
// nothing, and every word it shows is internal/decide's.
package board

import (
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Board is a run as baton shows it.
type Board struct {
	Title string `json:"title,omitempty"`
	Mode  string `json:"mode"` // the run's: idle (no plan), running, paused or complete
	// Step is the step the run is at, from 1: the first with a phase not done, or the last once every
	// phase is (0 with no plan).
	Step  int              `json:"step"`
	Steps []Step           `json:"steps,omitempty"`
	Lead  decide.LeadStand `json:"lead"`
	// Serial is why the run's phases go one at a time in the lead ("" when phase agents may work beside
	// it), and Limit the account's usage limit the run waits on.
	Serial string    `json:"serial,omitempty"`
	Limit  *Limit    `json:"limit,omitempty"`
	At     time.Time `json:"at"`
}

// Step is one step of the plan: phases none of which waits for another, in document order.
type Step struct {
	N      int     `json:"n"`
	Phases []Phase `json:"phases"`
}

// Phase is one phase of the plan, and how it stands.
type Phase struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Status string       `json:"status"` // the run's own (state.Phase*)
	Stand  decide.Stand `json:"stand"`
	// Agent is the phase agent at work on it, and Worker how its worker stands (state.Worker*), for a
	// phase given to one.
	Agent  string `json:"agent,omitempty"`
	Worker string `json:"worker,omitempty"`
}

// Limit is the account's usage limit the run waits on: who hit it (state.Usage.By), and until when.
type Limit struct {
	By    string    `json:"by,omitempty"`
	Until time.Time `json:"until"`
}

// Input is what a board is built from.
type Input struct {
	State    state.State
	Plan     plan.Plan
	HavePlan bool
	// Subs are the sub-runs of the run's workers (state.Project.Workers), where their phase agents work.
	Subs []state.RunInfo
	// Serial is why the run's phases go one at a time in the lead: the host's decision
	// (state.State.SerialWhy), or baton's config running no phase agents (decide.SerialConfig).
	Serial string
	Now    time.Time
}

// Build builds the board of the run in.
func Build(in Input) Board {
	st, pl, now := in.State, in.Plan, in.Now
	b := Board{Mode: st.Mode, At: now}
	if !in.HavePlan || st.Mode == state.ModeIdle || st.Mode == "" {
		b.Mode = state.ModeIdle
		return b
	}
	b.Title, b.Serial = pl.Title, in.Serial
	if u := st.Usage; u != nil && now.Before(u.Limited) {
		b.Limit = &Limit{By: u.By, Until: u.Limited}
	}
	subs := map[string]state.State{}
	for _, r := range in.Subs {
		subs[r.ID] = r.State
	}
	for i, ids := range pl.Steps() {
		s := Step{N: i + 1}
		for _, id := range ids {
			s.Phases = append(s.Phases, phase(st, pl, subs, id, now))
		}
		b.Steps = append(b.Steps, s)
	}
	b.Step = len(b.Steps)
	for _, s := range b.Steps {
		if slices.ContainsFunc(s.Phases, func(p Phase) bool { return p.Stand.Kind != decide.StandDone }) {
			b.Step = s.N
			break
		}
	}
	b.Lead = lead(st, now)
	return b
}

// phase is phase id of run st, and how it stands.
func phase(st state.State, pl plan.Plan, subs map[string]state.State, id string, now time.Time) Phase {
	p := Phase{ID: id, Status: state.PhasePending}
	if i := pl.Index(id); i >= 0 {
		p.Title = pl.Phases[i].Title
	}
	ps := st.Phases[id]
	if ps != nil {
		p.Status = ps.Status
	}
	switch {
	case p.Status == state.PhaseDone:
		p.Stand = decide.Stand{Kind: decide.StandDone}
	case p.Status == state.PhaseWorking:
		p.Stand = agent(st, subs, id, &p)
	case id == st.Current && st.BoundaryOwed:
		p.Stand = decide.Stand{Kind: decide.StandNext}
	case p.Status == state.PhaseActive && id == st.Current:
		p.Stand = decide.Stand{Kind: decide.StandLead, Since: ps.StartedAt}
		if j := st.CurrentJob(); j != nil {
			p.Stand.Job = j.ID
			if !st.Run.TurnOpen { // the lead's turn is over, and the phase waits on its gate
				p.Stand = gate(*j)
			}
		}
	case p.Status == state.PhaseVerifying:
		p.Stand = decide.Stand{Kind: decide.StandVerifying}
		if j := st.Latest(id); j != nil && !j.Finished() {
			p.Stand = gate(*j)
		}
	case p.Status == state.PhaseParked:
		p.Stand = parked(st, ps)
	default:
		p.Stand = pending(st, pl, id)
	}
	return p
}

// gate is how a phase stands while its job j runs: verifying, or awaiting something outside the project.
func gate(j state.Job) decide.Stand {
	if j.Kind == state.JobAwait {
		return decide.Stand{Kind: decide.StandAwaiting, Since: j.Queued, Job: j.ID}
	}
	return decide.Stand{Kind: decide.StandVerifying, Since: j.Queued, Job: j.ID}
}

// parked is how a phase stands that was set aside (ps.Park), in run st.
func parked(st state.State, ps *state.PhaseState) decide.Stand {
	s := decide.Stand{Kind: decide.StandParked}
	if pk := ps.Park; pk != nil {
		s.Since, s.Why = pk.Since, decide.ParkedWhy(pk.Reason)
		if j := st.Job(pk.Job); j != nil && !j.Finished() {
			s.Job = j.ID
		}
	}
	return s
}

// pending is how a phase stands that nobody has taken up: it waits on phases not done, a phase agent for
// it is held back, or it is ready.
func pending(st state.State, pl plan.Plan, id string) decide.Stand {
	var waits []string
	for _, d := range pl.Deps(id) {
		if ps := st.Phases[d]; ps == nil || ps.Status != state.PhaseDone {
			waits = append(waits, d)
		}
	}
	switch h := st.Hold; {
	case len(waits) > 0:
		return decide.Stand{Kind: decide.StandWaits, Waits: waits}
	case h != nil && h.Phase == id:
		return decide.Stand{Kind: decide.StandHeld, Since: h.Since, Why: h.Why}
	}
	return decide.Stand{Kind: decide.StandReady}
}

// agent is how phase id stands, given to a worker of run st: how its worker stands, and in its sub-run
// (subs), how its phase agent does. It fills in p's agent and worker.
func agent(st state.State, subs map[string]state.State, id string, p *Phase) decide.Stand {
	w := st.WorkerOn(id)
	if w == nil {
		return decide.Stand{Kind: decide.StandAgent}
	}
	p.Agent, p.Worker = w.AgentID, w.Status
	switch w.Status {
	case state.WorkerStarting:
		return decide.Stand{Kind: decide.StandStarting, Since: w.Since}
	case state.WorkerSuspended:
		return decide.Stand{Kind: decide.StandSuspended, Since: w.Since, Why: w.Why}
	case state.WorkerHeld:
		// The human stopped its agent: baton asks them whether to restart it, or the phase is theirs until
		// they ask for a fresh agent.
		return decide.Stand{Kind: decide.StandNeedsYou, Since: w.Since, Why: decide.AgentStoppedStand(*w, st.StopAsked(id))}
	case state.WorkerLanding:
		s := decide.Stand{Kind: decide.StandLanding, Since: w.Since}
		if m := w.Merge; m != nil {
			s.Since = m.Queued
			switch {
			case m.Reverify != "":
				s.Job = m.Reverify
			case m.Deferred != "":
				s.Why = m.Deferred
			}
		}
		return s
	}
	sub, ok := subs[w.Ref]
	if why := needs(st, w, sub, ok); why != "" {
		return decide.Stand{Kind: decide.StandNeedsYou, Why: why}
	}
	s := decide.Stand{Kind: decide.StandAgent, Since: w.Assigned}
	if !ok {
		return s
	}
	j := sub.Latest(id)
	if j != nil && j.Finished() {
		j = nil
	}
	switch ps := sub.Phases[id]; {
	case ps != nil && ps.Status == state.PhaseVerifying:
		if j != nil {
			return gate(*j)
		}
		return decide.Stand{Kind: decide.StandVerifying}
	case ps != nil && ps.Status == state.PhaseParked:
		return parked(sub, ps)
	case j != nil && !sub.Run.TurnOpen: // its turn is over, and it waits on its gate
		return gate(*j)
	}
	if j != nil {
		s.Job = j.ID // its gate runs while it works on
	}
	s.Idle = !sub.Run.TurnOpen && j == nil
	return s
}

// needs is what the phase agent of worker w needs of the human, or "": a permission prompt of its own on
// the lead's screen, a question baton's mod puts to the human for it, or in its sub-run (sub, when ok), a
// block, a review due, a proposal waiting (one held for the human too), or an escalation.
func needs(st state.State, w *state.Worker, sub state.State, ok bool) string {
	if w.AgentID != "" {
		for _, d := range st.Run.Dialogs {
			if d.Agent == w.AgentID && d.Kind == "" {
				return decide.NeedsPrompt(d.Tool)
			}
		}
	}
	for _, r := range st.Relay {
		if r.Phase != w.Phase || r.Answered != nil || r.AboutAgent() {
			continue // a question about a restarted agent only tells the human: the agent works on
		}
		switch r.Kind {
		case state.RelayBlocked:
			return decide.NeedsBlocked(r.Because)
		case state.RelayReview:
			return decide.NeedsReview(r.Phase)
		}
		return decide.NeedsProposal(r.Deadline, r.Outward != "")
	}
	if !ok {
		return ""
	}
	switch p := sub.PendingProposal(); {
	case sub.Blocked != nil:
		return decide.NeedsBlocked(sub.Blocked.Reason)
	case sub.ReviewDue != "":
		return decide.NeedsReview(sub.ReviewDue)
	case p != nil:
		return decide.NeedsProposal(p.Deadline, p.Outward != "")
	case sub.Run.Escalation != nil:
		return decide.NeedsEscalation(sub.Run.Escalation.Kind, sub.Run.Escalation.Reason)
	}
	return ""
}

// lead is how the lead session of run st stands.
func lead(st state.State, now time.Time) decide.LeadStand {
	switch st.Mode {
	case state.ModePaused:
		return decide.LeadStand{Kind: decide.LeadPaused}
	case state.ModeComplete:
		return decide.LeadStand{Kind: decide.LeadDone}
	}
	if why := leadNeeds(st); why != "" {
		return decide.LeadStand{Kind: decide.LeadNeedsYou, Phase: st.Current, Why: why}
	}
	switch c := st.Run.Compaction; {
	case c.InFlight() && st.Run.Gate != "":
		// Never claim a compaction is happening while something holds it back (statusline.Segment).
		return decide.LeadStand{Kind: decide.LeadHeld, Why: st.Run.Gate}
	case c.InFlight():
		since := c.Started
		if since.IsZero() {
			since = c.Typed
		}
		return decide.LeadStand{Kind: decide.LeadCompacting, Since: since}
	case st.BoundaryOwed:
		return decide.LeadStand{Kind: decide.LeadOwed, Phase: st.Current}
	case st.CheckpointOwed || st.CheckpointDue:
		return decide.LeadStand{Kind: decide.LeadOwed}
	}
	if st.Current != "" {
		switch j := st.CurrentJob(); {
		case st.Run.TurnOpen:
			return decide.LeadStand{Kind: decide.LeadWorking, Phase: st.Current, Since: st.Run.TurnStarted}
		case j != nil:
			return decide.LeadStand{Kind: decide.LeadWaiting, Phase: st.Current, Since: j.Queued, On: []string{j.ID}}
		}
		return decide.LeadStand{Kind: decide.LeadIdle, Phase: st.Current}
	}
	// No phase in hand: every phase left waits on a job, or on the phase agents at work.
	var on []string
	for _, j := range st.Unfinished() {
		on = append(on, j.ID)
	}
	for _, w := range st.LiveWorkers() {
		if w.Generation == st.Generation {
			on = append(on, w.Phase)
		}
	}
	if len(on) > 0 {
		return decide.LeadStand{Kind: decide.LeadWaiting, On: on}
	}
	if st.Run.TurnOpen {
		return decide.LeadStand{Kind: decide.LeadWorking, Since: st.Run.TurnStarted}
	}
	return decide.LeadStand{Kind: decide.LeadIdle}
}

// leadNeeds is what the lead session of run st needs of the human, or "", in the order the status line
// puts it: a review due, a proposal, an escalation, a block. (Its own dialogs are on the human's screen.)
func leadNeeds(st state.State) string {
	p := st.PendingProposal()
	switch e := st.Run.Escalation; {
	case st.ReviewDue != "":
		return decide.NeedsReview(st.ReviewDue)
	case p != nil && !p.Deadline.IsZero():
		return decide.NeedsProposal(p.Deadline, p.Outward != "")
	case p != nil || e != nil && e.Kind == "decision":
		return decide.NeedsProposal(time.Time{}, false)
	case e != nil:
		return decide.NeedsEscalation(e.Kind, e.Reason)
	case st.Blocked != nil:
		return decide.NeedsBlocked(st.Blocked.Reason)
	}
	return ""
}
