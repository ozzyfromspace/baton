package loop

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton's mod (internal/bridge) is what starts phases beside the lead, inside the lead's claude. Until it
// has said hello there, nothing starts beside the lead. With no hello within Timing.ModHello of the lead's
// session starting while a plan runs, or a hello from a mod baton does not work with, the run is serial:
// its phases go one at a time in the lead session, as with workers: 0, and the human is told once why.

// modWait says why no phase starts beside the lead for want of baton's mod ("" once the mod in the lead's
// claude has said hello), and makes the run serial when it is time to. Only a fleet step calls it.
func (c *Coordinator) modWait(now time.Time, s *state.Store, st state.State) string {
	if c.modReady {
		return ""
	}
	rec, err := bridge.Open(s.Dir, s.Now, nil).Peek()
	h := rec.Hello
	switch {
	case err == nil && h.Ready(s.Instance):
		c.modReady = true
		return ""
	case st.Mode != state.ModeRunning:
		return "baton's mod has not said hello yet"
	case err == nil && h != nil && h.Instance == s.Instance:
		c.serial(now, s, h.Refused)
		return c.off
	}
	if c.modSince.IsZero() {
		c.modSince = now
	}
	if now.Sub(c.modSince) >= c.Lead.Timing.ModHello {
		c.serial(now, s, decide.ModMissing)
		return c.off
	}
	return "baton's mod has not said hello yet"
}

// serial makes the run's phases go one at a time in the lead, for why, and tells the human once.
func (c *Coordinator) serial(now time.Time, s *state.Store, why string) {
	if c.off != "" {
		return
	}
	c.off = why
	line := decide.Serial(why)
	s.Update(func(st *state.State) error {
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "workers", Text: line, At: now})
		st.Run.Announce = append(st.Run.Announce, line)
		st.Run.Serial = &state.Serial{Instance: s.Instance, Why: why, At: now} // baton's board says so
		return nil
	})
	s.Event("serial", map[string]any{"why": why})
}
