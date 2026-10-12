package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// helloFrom records that baton's mod said hello in the claude of s's host instance.
func helloFrom(t *testing.T, s *state.Store) {
	t.Helper()
	say(t, s, bridge.HelloInfo{Instance: s.Instance, Version: "0.8.0", Protocol: bridge.Protocol})
}

func say(t *testing.T, s *state.Store, h bridge.HelloInfo) {
	t.Helper()
	if _, err := bridge.Open(s.Dir, s.Now, nil).SayHello(h); err != nil {
		t.Fatal(err)
	}
}

// Phases start beside the lead only once baton's mod has said hello in the lead's claude. With none
// within Timing.ModHello of a plan running there, or one from a mod baton does not work with, the run is
// serial: no worker starts, and the human is told once why. With no plan running, or workers off, baton
// waits and says nothing.
func TestARunIsSerialWithoutTheMod(t *testing.T) {
	mismatch := decide.ModMismatch("0.9.0", "v0.8.0")
	for _, c := range []struct {
		name    string
		hello   *bridge.HelloInfo // said before the first tick (nil: none)
		late    bool              // a hello from the lead's claude comes 10 seconds in
		paused  bool              // no plan runs
		off     bool              // workers: 0
		workers int               // started within a minute
		serial  string            // why the run is serial ("" for not)
	}{
		{name: "a hello", hello: &bridge.HelloInfo{Instance: "inst", Protocol: bridge.Protocol}, workers: 1},
		{name: "a hello within the wait", late: true, workers: 1},
		{name: "no hello", serial: decide.ModMissing},
		{name: "a hello from another claude", hello: &bridge.HelloInfo{Instance: "inst-gone", Protocol: bridge.Protocol}, serial: decide.ModMissing},
		{name: "a mod of another baton", hello: &bridge.HelloInfo{Instance: "inst", Protocol: bridge.Protocol + 1, Refused: mismatch}, serial: mismatch},
		{name: "no plan running", paused: true},
		{name: "workers off", off: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newCrew(t, 1)
			pushed := &pushes{}
			x.c.Lead.Notify = pushed
			os.RemoveAll(filepath.Join(x.lead.Dir, "mod")) // the crew's mod said hello
			if c.hello != nil {
				say(t, x.lead, *c.hello)
			}
			if c.paused {
				x.lead.Update(func(st *state.State) error { st.Mode = state.ModePaused; return nil })
			}
			if c.off {
				x.c.Fleet = nil
			}
			x.tick(time.Second)
			for range 5 {
				x.tick(2 * time.Second)
			}
			if c.late {
				helloFrom(t, x.lead)
			}
			if n := len(x.spawns()); c.serial != "" && n != 0 {
				t.Fatalf("%d workers before the wait was over", n)
			}
			if strings.Contains(x.events(), `"kind":"serial"`) && c.serial != mismatch {
				t.Fatalf("serial before the wait was over: %s", x.events())
			}
			for range 30 {
				x.tick(2 * time.Second)
			}
			helloFrom(t, x.lead) // too late for a serial run
			x.tick(2 * time.Second)
			if len(x.spawns()) != c.workers {
				t.Errorf("%d workers, want %d", len(x.spawns()), c.workers)
			}
			ev, st := x.events(), x.state()
			announced := 0
			for _, a := range st.Run.Announce {
				if strings.Contains(a, "phases run one at a time") {
					announced++
					if a != decide.Serial(c.serial) {
						t.Errorf("announced %q, want %q", a, decide.Serial(c.serial))
					}
				}
			}
			if c.serial == "" {
				if announced != 0 || strings.Contains(ev, `"kind":"serial"`) {
					t.Errorf("serial: announced %d; events:\n%s", announced, ev)
				}
				return
			}
			if announced != 1 || strings.Count(ev, `"kind":"serial"`) != 1 || !strings.Contains(ev, `"why":"`+c.serial[:20]) {
				t.Errorf("announced %d; events:\n%s", announced, ev)
			}
			if p := strings.Join(pushed.details, "\n"); strings.Count(p, "one at a time") != 1 {
				t.Errorf("pushed %q", p)
			}
		})
	}
}
