package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton's view of a run says (internal/board): the band, one line above the lead's prompt, and the
// board, the whole plan step by step. A step is a level of the plan's graph: phases that may run at once.
// Each phase stands one way (Stand), and so does the lead session (LeadStand).

// BandMark heads the band and the board.
const BandMark = "◆ baton"

// How a phase stands (Stand.Kind).
const (
	StandDone      = "done"      // its work is in: done by the lead, or landed
	StandLead      = "lead"      // the lead session works on it
	StandNext      = "next"      // the lead takes it up once its conversation has compacted
	StandAgent     = "agent"     // a phase agent works on it
	StandStarting  = "starting"  // baton's mod was asked to start its agent
	StandSuspended = "suspended" // its agent ended with the lead's claude, and the next host starts one again
	StandVerifying = "verifying" // its gate runs
	StandAwaiting  = "awaiting"  // it waits on something outside the project
	StandParked    = "parked"    // set aside until its job ends, or after its gate failed
	StandLanding   = "landing"   // its agent's work waits in the merge queue
	StandHeld      = "held"      // it could start beside the lead, and something holds its agent back
	StandReady     = "ready"     // it can start, and waits its turn
	StandWaits     = "waits"     // it waits on other phases
	StandNeedsYou  = "needs_you" // it waits on the human
)

// Stand is how one phase of a run stands.
type Stand struct {
	Kind  string    `json:"kind"`
	Since time.Time `json:"since,omitzero"` // since when it has stood so, where that says something
	Job   string    `json:"job,omitempty"`  // the job it waits on
	Also  string    `json:"also,omitempty"` // the job of the other kind it waits on beside Job
	// Waits are the phases it waits on; Why is what it needs of the human, or why it is held back, set
	// aside, suspended or waits to land, or, ready, why no agent takes it up yet.
	Waits []string `json:"waits,omitempty"`
	Why   string   `json:"why,omitempty"`
	// Idle: its agent's turn is over, and nothing runs for it.
	Idle bool `json:"idle,omitempty"`
}

// Mark is the phase's mark on the board.
func (s Stand) Mark() string {
	switch s.Kind {
	case StandDone:
		return "✓"
	case StandLead:
		return "▶"
	case StandNext:
		return "→"
	case StandAgent, StandStarting, StandLanding:
		return "⚙"
	case StandVerifying, StandAwaiting:
		return "⧗"
	case StandParked, StandSuspended:
		return "⏸"
	case StandNeedsYou:
		return "?"
	}
	return "·"
}

// Band is how phase id stands, in a few words for the band: "P3 ⚙ 12m", "P4 verifying 3m", "P5 waits on
// P3".
func (s Stand) Band(id string, now time.Time) string {
	switch s.Kind {
	case StandDone:
		return id + " ✓"
	case StandLead:
		return words(id, "▶", since(s.Since, now))
	case StandNext:
		return id + " next"
	case StandAgent:
		return words(id, "⚙", since(s.Since, now))
	case StandStarting:
		return id + " starting"
	case StandSuspended:
		return id + " suspended"
	case StandVerifying:
		return words(id, "verifying", since(s.Since, now))
	case StandAwaiting:
		return words(id, "awaiting", since(s.Since, now))
	case StandParked:
		return id + " parked"
	case StandLanding:
		switch {
		case s.Job != "":
			return id + " verifying again"
		case s.Why != "":
			return id + " waits to land"
		}
		return id + " landing"
	case StandHeld:
		return id + " held"
	case StandReady:
		return id + " ready"
	case StandWaits:
		return id + " waits on " + strings.Join(s.Waits, ", ")
	case StandNeedsYou:
		return id + " needs you"
	}
	return id
}

// Board is how a phase stands, for its row on the board: "agent at work 12m", "verifying 3m (job j4)",
// "waits on P3 and P4", "needs you: blocked: the signing key is gone".
func (s Stand) Board(now time.Time) string {
	job := ""
	switch {
	case s.Job != "" && s.Also != "":
		job = "(jobs " + s.Job + " and " + s.Also + ")"
	case s.Job != "":
		job = "(job " + s.Job + ")"
	}
	switch s.Kind {
	case StandDone:
		return "done"
	case StandLead:
		return words("the lead works on it", since(s.Since, now), job)
	case StandNext:
		return "the lead's next, once its conversation has compacted"
	case StandAgent:
		if s.Idle {
			return words("agent between turns", since(s.Since, now))
		}
		return words("agent at work", since(s.Since, now))
	case StandStarting:
		return "agent starting"
	case StandSuspended:
		return words("agent suspended:", s.Why, "(the next baton session of this run starts it again)")
	case StandVerifying:
		return words("verifying", since(s.Since, now), job)
	case StandAwaiting:
		return words("awaiting", since(s.Since, now), job)
	case StandParked:
		return words("set aside", s.Why, job)
	case StandLanding:
		switch {
		case s.Job != "":
			return words("verifying again with the run's branch merged in", job)
		case s.Why != "":
			return "waits to land: " + s.Why
		}
		return "landing"
	case StandHeld:
		return words("held:", s.Why)
	case StandReady:
		if s.Why != "" {
			return "ready: " + s.Why
		}
		return "ready"
	case StandWaits:
		return "waits on " + andList(s.Waits)
	case StandNeedsYou:
		return words("needs you:", s.Why)
	}
	return s.Kind
}

// SlotsAtWork is why a phase that could go ahead waits, every one of the n agent slots (workers, or what
// workers: auto allows on this machine) having a phase agent at work: "all 6 agent slots at work".
func SlotsAtWork(n int) string {
	if n == 1 {
		return "the one agent slot at work"
	}
	return fmt.Sprintf("all %d agent slots at work", n)
}

// How the lead session stands (LeadStand.Kind).
const (
	LeadWorking    = "working"    // its turn runs
	LeadWaiting    = "waiting_on" // its turn is over, and it waits on jobs or phase agents
	LeadIdle       = "idle"       // its turn is over, and nothing runs for it
	LeadCompacting = "compacting" // its conversation compacts
	LeadHeld       = "held"       // a compaction is due, and something holds it back
	LeadOwed       = "owed"       // a compaction is due at its next stop
	LeadNeedsYou   = "needs_you"  // it waits on the human
	LeadPaused     = "paused"     // the human took the wheel
	LeadDone       = "done"       // the plan is complete
)

// LeadStand is how the lead session stands.
type LeadStand struct {
	Kind  string    `json:"kind"`
	Phase string    `json:"phase,omitempty"` // the phase in hand
	Since time.Time `json:"since,omitzero"`  // since when it has stood so, where that says something
	On    []string  `json:"on,omitempty"`    // the jobs and phases it waits on
	Why   string    `json:"why,omitempty"`   // what it needs of the human, or what holds its compaction back
}

// Band is the lead's part of the band: "lead: working on P2", "lead: waiting on j4".
func (l LeadStand) Band() string {
	switch l.Kind {
	case LeadWorking:
		if l.Phase != "" {
			return "lead: working on " + l.Phase
		}
		return "lead: working"
	case LeadWaiting:
		return "lead: waiting on " + strings.Join(l.On, ", ")
	case LeadCompacting:
		return "lead: compacting"
	case LeadHeld:
		return "lead: compaction held"
	case LeadOwed:
		return "lead: compacts at its next stop"
	case LeadNeedsYou:
		return "lead: needs you"
	case LeadPaused:
		return "lead: paused"
	case LeadDone:
		return "lead: done"
	}
	return "lead: idle"
}

// Board is the lead's line on the board.
func (l LeadStand) Board(now time.Time) string {
	switch l.Kind {
	case LeadWorking:
		if l.Phase != "" {
			return words("lead: working on "+l.Phase+", its turn", since(l.Since, now))
		}
		return words("lead: working, its turn", since(l.Since, now))
	case LeadWaiting:
		return "lead: its turn over, waiting on " + andList(l.On)
	case LeadIdle:
		if l.Phase != "" {
			return "lead: its turn over, with " + l.Phase + " in hand"
		}
		return "lead: idle, with no phase in hand"
	case LeadCompacting:
		return words("lead: compacting", since(l.Since, now))
	case LeadHeld:
		return "lead: a compaction is due, held back: " + l.Why
	case LeadOwed:
		if l.Phase != "" {
			return "lead: compacts at its next stop, then takes up " + l.Phase
		}
		return "lead: compacts at its next stop"
	case LeadNeedsYou:
		return words("lead: needs you:", l.Why)
	case LeadPaused:
		return "lead: paused, the human has the wheel (/baton run resumes the plan)"
	case LeadDone:
		return "lead: the plan is complete"
	}
	return "lead: " + l.Kind
}

// What a phase or the lead needs of the human (Stand.Why, LeadStand.Why).

// NeedsReview: a review of a phase's decisions is due.
func NeedsReview(phase string) string { return "a review of " + phase + "'s decisions is due" }

// NeedsProposal: a proposal waits for the human until its deadline (zero: one held for them), when it goes
// ahead, or is answered Not now if it is outward.
func NeedsProposal(deadline time.Time, outward bool) string {
	switch {
	case deadline.IsZero():
		return "a proposal waits for your answer"
	case outward:
		return "a proposal, answered Not now at " + deadline.Local().Format("15:04")
	}
	return "a proposal, which goes ahead at " + deadline.Local().Format("15:04")
}

// NeedsBlocked: the phase is blocked, for reason.
func NeedsBlocked(reason string) string { return "blocked: " + reason }

// NeedsAgentStopped: the human stopped the phase's agent (state.WorkerHeld), and the phase waits for them.
const NeedsAgentStopped = "its agent was stopped; /baton run starts a fresh one"

// NeedsPrompt: a permission prompt for tool waits in the lead's terminal.
func NeedsPrompt(tool string) string {
	if tool == "" {
		return "a permission prompt"
	}
	return "a permission prompt (" + tool + ")"
}

// NeedsEscalation: an escalation of kind waits for the human, for reason.
func NeedsEscalation(kind, reason string) string {
	switch {
	case reason != "":
		return reason
	case kind != "":
		return kind
	}
	return "baton waits for your answer"
}

// ParkedWhy is why a phase was set aside (state.Park.Reason), for Stand.Why: "while its job runs".
func ParkedWhy(reason string) string {
	switch reason {
	case state.ParkJob:
		return "while its job runs"
	case state.ParkRed:
		return "after its verification failed"
	case state.ParkUnverified:
		return "after its verification had no verdict"
	case state.ParkCheckpoint:
		return "at a checkpoint"
	}
	return ""
}

// The band's other words.

// BandStep is the step the run is at, of how many: "step 2/4".
func BandStep(n, of int) string { return fmt.Sprintf("step %d/%d", n, of) }

// BandLimit: the run waits on the account's usage limit until until, as of now, and what its resume timer
// says (state/resume.go): it resumes the run at resumes ("usage limit · resumes at 17:02"), or was
// cancelled ("usage limit until 17:02 · auto-resume cancelled"). The status line and baton status say it
// too.
func BandLimit(until, resumes time.Time, cancelled bool, now time.Time) string {
	switch {
	case !resumes.IsZero():
		return "usage limit · resumes at " + Clock(resumes, now)
	case cancelled:
		return "usage limit until " + Clock(until, now) + " · auto-resume cancelled"
	}
	return "usage limit until " + Clock(until, now)
}

// BandMore counts the phases the band had no room for.
func BandMore(n int) string { return fmt.Sprintf("+%d", n) }

const (
	BandPaused   = "paused"
	BandComplete = "✓ plan complete"
	BandSerial   = "one at a time" // the phases go one at a time in the lead
)

// The board's other words.

// BoardHead is the board's first line: the plan, how the run goes, and its step.
func BoardHead(title, mode string, step, steps int) string {
	head := fmt.Sprintf("%s · %q", BandMark, title)
	switch mode {
	case state.ModeComplete:
		return head + " · " + BandComplete
	case state.ModePaused:
		return fmt.Sprintf("%s · paused · step %d of %d", head, step, steps)
	}
	return fmt.Sprintf("%s · step %d of %d", head, step, steps)
}

// BoardStep labels a step's first row: "step 2".
func BoardStep(n int) string { return fmt.Sprintf("step %d", n) }

// BoardSerial says why the run's phases go one at a time in the lead.
func BoardSerial(why string) string { return "phases run one at a time here: " + why }

// BoardLimit says the run waits on the account's usage limit, which by hit, until until, as of now:
// nothing starts, and nothing is typed into the lead. Its resume timer resumes the run at resumes, or was
// cancelled.
func BoardLimit(by string, until, resumes time.Time, cancelled bool, now time.Time) string {
	who := "the lead"
	if by != "" && by != "lead" {
		who = "the agent on " + by
	}
	s := fmt.Sprintf("usage limit: %s hit it; nothing starts, and baton types nothing into the lead, until %s", who, Clock(until, now))
	switch {
	case !resumes.IsZero():
		s += "; baton resumes the run at " + Clock(resumes, now) + " unless you pause or write"
	case cancelled:
		s += "; auto-resume cancelled: /baton run resumes"
	}
	return s
}

// BoardNoPlan is the board of a run with no plan.
const BoardNoPlan = "baton: no plan attached here."

// SerialConfig is why phases run one at a time in the lead when baton's config runs no phase agents:
// workers is 0, or auto on a machine with room for none.
func SerialConfig(auto bool) string {
	if auto {
		return "workers is auto in baton's config, and this machine has room for none"
	}
	return "workers is 0 in baton's config"
}

// since is how long since t, as of now ("" when t is zero).
func since(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return Span(max(now.Sub(t), 0))
}

// words joins the words that are not empty with spaces.
func words(w ...string) string {
	out := w[:0:0]
	for _, s := range w {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}
