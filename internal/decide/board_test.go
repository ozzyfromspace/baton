package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Each way a phase stands, in the band's few words, the board's, and its mark.
func TestStandWords(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	for _, c := range []struct {
		s                 Stand
		mark, band, board string
	}{
		{Stand{Kind: StandDone}, "✓", "P3 ✓", "done"},
		{Stand{Kind: StandLead, Since: ago(12 * time.Minute)}, "▶", "P3 ▶ 12m", "the lead works on it 12m"},
		{Stand{Kind: StandLead, Since: ago(12 * time.Minute), Job: "j4"}, "▶", "P3 ▶ 12m", "the lead works on it 12m (job j4)"},
		{Stand{Kind: StandNext}, "→", "P3 next", "the lead's next, once its conversation has compacted"},
		{Stand{Kind: StandAgent, Since: ago(time.Hour + 5*time.Minute)}, "⚙", "P3 ⚙ 1h05m", "agent at work 1h05m"},
		{Stand{Kind: StandAgent, Since: ago(time.Minute), Idle: true}, "⚙", "P3 ⚙ 1m", "agent between turns 1m"},
		{Stand{Kind: StandAgent}, "⚙", "P3 ⚙", "agent at work"},
		{Stand{Kind: StandStarting}, "⚙", "P3 starting", "agent starting"},
		{Stand{Kind: StandSuspended, Why: "claude exited"}, "⏸", "P3 suspended",
			"agent suspended: claude exited (the next baton session of this run starts it again)"},
		{Stand{Kind: StandVerifying, Since: ago(3 * time.Minute), Job: "P3.j1"}, "⧗", "P3 verifying 3m", "verifying 3m (job P3.j1)"},
		{Stand{Kind: StandAwaiting, Since: ago(40 * time.Second), Job: "j2"}, "⧗", "P3 awaiting 40s", "awaiting 40s (job j2)"},
		{Stand{Kind: StandParked, Why: ParkedWhy(state.ParkJob), Job: "j2"}, "⏸", "P3 parked", "set aside while its job runs (job j2)"},
		{Stand{Kind: StandParked, Why: ParkedWhy(state.ParkRed)}, "⏸", "P3 parked", "set aside after its verification failed"},
		{Stand{Kind: StandLanding}, "⚙", "P3 landing", "landing"},
		{Stand{Kind: StandLanding, Why: "the lead changed a.go too"}, "⚙", "P3 waits to land", "waits to land: the lead changed a.go too"},
		{Stand{Kind: StandLanding, Job: "P3.j2"}, "⚙", "P3 verifying again", "verifying again with the run's branch merged in (job P3.j2)"},
		{Stand{Kind: StandHeld, Why: "memory pressure is critical"}, "·", "P3 held", "held: memory pressure is critical"},
		{Stand{Kind: StandReady}, "·", "P3 ready", "ready"},
		{Stand{Kind: StandWaits, Waits: []string{"P1", "P2"}}, "·", "P3 waits on P1, P2", "waits on P1 and P2"},
		{Stand{Kind: StandNeedsYou, Why: NeedsPrompt("Bash")}, "?", "P3 needs you", "needs you: a permission prompt (Bash)"},
		{Stand{Kind: StandNeedsYou, Why: NeedsProposal(time.Time{})}, "?", "P3 needs you", "needs you: a proposal waits for your answer"},
	} {
		if got := c.s.Mark(); got != c.mark {
			t.Errorf("%+v: mark %q, want %q", c.s, got, c.mark)
		}
		if got := c.s.Band("P3", now); got != c.band {
			t.Errorf("%+v: band %q, want %q", c.s, got, c.band)
		}
		if got := c.s.Board(now); got != c.board {
			t.Errorf("%+v: board %q, want %q", c.s, got, c.board)
		}
	}
}

// How the lead stands, on the band and on the board.
func TestLeadWords(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		l           LeadStand
		band, board string
	}{
		{LeadStand{Kind: LeadWorking, Phase: "P2", Since: now.Add(-3 * time.Minute)}, "lead: working on P2", "lead: working on P2, its turn 3m"},
		{LeadStand{Kind: LeadWorking}, "lead: working", "lead: working, its turn"},
		{LeadStand{Kind: LeadWaiting, On: []string{"j4", "P3"}}, "lead: waiting on j4, P3", "lead: its turn over, waiting on j4 and P3"},
		{LeadStand{Kind: LeadIdle, Phase: "P2"}, "lead: idle", "lead: its turn over, with P2 in hand"},
		{LeadStand{Kind: LeadIdle}, "lead: idle", "lead: idle, with no phase in hand"},
		{LeadStand{Kind: LeadCompacting, Since: now.Add(-time.Minute)}, "lead: compacting", "lead: compacting 1m"},
		{LeadStand{Kind: LeadHeld, Why: "a dialog is open"}, "lead: compaction held", "lead: a compaction is due, held back: a dialog is open"},
		{LeadStand{Kind: LeadOwed, Phase: "P3"}, "lead: compacts at its next stop", "lead: compacts at its next stop, then takes up P3"},
		{LeadStand{Kind: LeadOwed}, "lead: compacts at its next stop", "lead: compacts at its next stop"},
		{LeadStand{Kind: LeadNeedsYou, Why: NeedsBlocked("no key")}, "lead: needs you", "lead: needs you: blocked: no key"},
		{LeadStand{Kind: LeadPaused}, "lead: paused", "lead: paused, the human has the wheel (/baton run resumes the plan)"},
		{LeadStand{Kind: LeadDone}, "lead: done", "lead: the plan is complete"},
	} {
		if got := c.l.Band(); got != c.band {
			t.Errorf("%+v: band %q, want %q", c.l, got, c.band)
		}
		if got := c.l.Board(now); got != c.board {
			t.Errorf("%+v: board %q, want %q", c.l, got, c.board)
		}
	}
}
