package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's block question tells the human what the rest of the run does meanwhile, as the host sees
// it when it has the mod ask: it goes on while something else can move (here the agent on P2), and when
// nothing can, it says so and names the phases that wait on the blocked one. The question the mod is told
// to ask is the one recorded, by which its answer is matched, and the event says which it was.
func TestABlockQuestionTellsTheTruthAboutTheRun(t *testing.T) {
	for _, c := range []struct {
		name  string
		max   int      // agent slots
		work  []string // the phases whose agents are at work, the blocked P1 first
		done  []string // the phases done
		says  string
		stuck bool
		moves string
	}{
		{"another agent at work", 2, []string{"P1", "P2"}, []string{"P0"},
			"Only P1 waits for you; the rest of the run goes on.", false, "the agent on P2 is at work"},
		{"the lead holds a phase", 1, []string{"P1"}, nil,
			"Only P1 waits for you; the rest of the run goes on.", false, "the lead holds P0"},
		{"the lead takes the next phase", 1, []string{"P1"}, []string{"P0"},
			"Only P1 waits for you; the rest of the run goes on.", false, "the lead holds P2"},
		{"nothing else can move", 1, []string{"P1"}, []string{"P0", "P2"},
			"Nothing else in the run can move until it is cleared: P3 waits on P1.", true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, c.max)
			x.atWork(c.work...)
			x.lead.Update(func(st *state.State) error {
				for _, id := range c.done {
					st.Phases[id].Status = state.PhaseDone
					if st.Current == id {
						st.Current = ""
					}
				}
				return nil
			})
			sub := x.sub("P1")
			sub.Update(func(st *state.State) error {
				state.Start(st, x.now, state.Origin{})
				if err := state.SetBlocked(st, "need a key", x.now); err != nil {
					return err
				}
				st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "blocked on P1: need a key", Since: x.now, Asked: true}
				st.Run.TurnOpen, st.Run.LastActivity, st.Run.LastStop = false, x.now, x.now
				return nil
			})
			if err := sub.Relay(state.Relay{Kind: state.RelayBlocked, Since: x.now, Because: "need a key"}); err != nil {
				t.Fatal(err)
			}
			x.tick(time.Second)
			if len(x.asks) != 1 {
				t.Fatalf("asked %d times: %+v\n%s", len(x.asks), x.asks, x.events())
			}
			st := x.state()
			r := st.RelayIssued()
			if r == nil || r.Kind != state.RelayBlocked {
				t.Fatalf("issued %+v", r)
			}
			if r.Question != x.asks[0].Question {
				t.Errorf("the question asked:\n%s\nis not the one recorded:\n%s", x.asks[0].Question, r.Question)
			}
			if !strings.Contains(r.Question, "baton: the agent on P1 is blocked: need a key. "+c.says+" Unless you answer by ") {
				t.Errorf("the question:\n%s\ndoes not say %q", r.Question, c.says)
			}
			if r.RunWaits != c.stuck {
				t.Errorf("recorded run_waits %v, want %v", r.RunWaits, c.stuck)
			}
			fields := map[string]any{"phase": "P1", "type": state.RelayBlocked, "via": "mod", "run_waits": c.stuck}
			if c.moves != "" {
				fields["moves"] = c.moves
			}
			if !x.logged("relay_issued", fields) || c.moves == "" && strings.Contains(x.events(), `"moves"`) {
				t.Errorf("no relay_issued with %v:\n%s", fields, x.events())
			}
		})
	}
}
