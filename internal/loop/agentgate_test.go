package loop

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent's loop types nothing: its lines are messages baton's mod sends, which Claude Code holds for
// the agent's next tool round. So no screen holds it, however busy, and it never logs that it types anyway;
// its events say its lines went by the mod. The lead's loop where it types still waits for its screen to
// settle, up to QuietMax, and then types anyway, once logged.
func TestAnAgentsLoopHasNoScreenToSettle(t *testing.T) {
	for _, c := range []struct {
		name   string
		agent  bool
		via    string
		screen time.Duration // how long a screen that never settles holds the loop
	}{
		{"a phase agent's", true, "mod", 0},
		{"the lead's, typing", false, "keys", DefaultTiming.QuietMax},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newFleet(t)
			var logged []string
			logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
			s, l := x.lead, &Loop{Store: x.lead, Timing: DefaultTiming, Logf: logf}
			if c.agent {
				s, l = x.work, x.agent.loop
				l.Logf = logf
			}
			st, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			start := x.now
			var open time.Time // when the gate first let the loop type
			for x.now.Sub(start) < DefaultTiming.QuietMax+5*time.Second {
				x.now = x.now.Add(time.Second)
				why := l.gate(host.View{Now: x.now, Store: s, Owner: true, LastOutput: x.now}, st)
				if why != "" && why != gateScreen {
					t.Fatalf("held by %q", why)
				}
				if why == "" && open.IsZero() {
					open = x.now
				}
			}
			if held := open.Sub(start) - time.Second; held != c.screen {
				t.Errorf("the screen held it %s, want %s", held, c.screen)
			}
			if n := strings.Count(strings.Join(logged, "\n"), "typing anyway"); n != min(int(c.screen), 1) {
				t.Errorf("logged typing anyway %d times: %q", n, logged)
			}
			if n := strings.Count(x.events(s), `"kind":"screen_never_quiet"`); n != min(int(c.screen), 1) {
				t.Errorf("%d screen_never_quiet events:\n%s", n, x.events(s))
			}
			if got := l.via(); got != c.via {
				t.Errorf("via %q, want %q", got, c.via)
			}
		})
	}
}

// At work, an agent whose hooks keep firing while its turn is over (Runtime.LastActivity) is open for its
// line at every tick: nothing about its activity holds it as a screen would.
func TestAnAgentsActivityDoesNotHoldItsLine(t *testing.T) {
	x := newFleet(t)
	var logged []string
	x.agent.loop.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	x.agent.in = &typed{}
	for range int(DefaultTiming.QuietMax/time.Second) + 5 {
		x.set(x.work, func(st *state.State) { st.Run.TurnOpen, st.Run.LastActivity = false, x.now })
		x.tick(time.Second)
		if why := x.agent.loop.why; why != "" {
			t.Fatalf("held by %q", why)
		}
	}
	if strings.Contains(strings.Join(logged, "\n"), "typing anyway") || strings.Contains(x.events(x.work), "screen_never_quiet") {
		t.Errorf("logged a screen that never settles:\n%q\n%s", logged, x.events(x.work))
	}
}
