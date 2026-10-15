package loop

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
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

// moved tells baton's mod in this claude that the host drives run to now, not from: the mod polls the
// bridge of the run it said hello in, and would never see to's commands. A Moved command in from's bridge
// has it say hello again, which gives it to's (bridge.Moved). One such command this host left in to's
// bridge, when it drove elsewhere before, is taken back: the mod is about to say hello there anyway.
func moved(from, to *state.Store, logf func(string, ...any)) {
	b := bridge.Open(from.Dir, from.Now, func(kind string, fields map[string]any) { from.Event(kind, fields) })
	if rec, err := b.Peek(); err == nil && rec.Hello.Ready(from.Instance) {
		if c, err := b.Issue(bridge.Command{Kind: bridge.Moved, Instance: from.Instance}); err != nil {
			logf("loop: telling the mod the host drives another run: %v", err)
		} else {
			logf("loop: told the mod the host drives another run (command %d)", c.ID)
		}
	}
	if _, err := bridge.Open(to.Dir, to.Now, nil).Retract(to.Instance, bridge.Moved); err != nil {
		logf("loop: taking back a move told before: %v", err)
	}
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

// The lead's loop reaches the lead's claude through baton's mod where it runs: the mod compacts the lead
// (bridge.Compact) and submits baton's lines to it (bridge.Submit) from inside, between turns, with no key
// typed and the human's draft left in the input box (spike 35). Where no mod runs (serial mode), the loop
// types, as v0.7 did. Which it is, the mod's hello in the run's bridge says:
const (
	modOff     = iota // no mod in this claude: baton types
	modOn             // the mod in this claude said hello: baton types nothing
	modWaiting        // a mod said hello in this run before, and the one in this claude may yet: baton waits
)

// modAckWait is how long the mod has to ack one of the lead's commands before baton takes it back.
const modAckWait = time.Minute

// modEvery spaces out looks at the bridge for a hello while none has come.
const modEvery = time.Second

// errSubmitOpen holds a line back while the one before it is not acked yet.
var errSubmitOpen = errors.New("the line before it is not acked yet")

// modMode says how this tick reaches the lead's claude. Once its mod has said hello, it does so for the
// rest of this host's life. A hello from this claude's mod baton does not work with (another protocol)
// means none; with no hello in this claude yet, one in the run from an earlier claude (a restart, a
// resume) means its mod is about to say hello, and baton waits for it up to Timing.ModHello from the host's
// first tick. A phase agent's loop has no claude of its own: its line is a message.
func (l *Loop) modMode(v host.View) int {
	switch {
	case l.Store.Worker() != "":
		return modOff
	case l.modOn:
		return modOn
	case !l.modAt.IsZero() && v.Now.Sub(l.modAt) < modEvery:
		return l.mode
	}
	l.modAt = v.Now
	rec, err := l.bridge().Peek()
	h := rec.Hello
	switch {
	case err != nil || h == nil:
		return modOff
	case h.Ready(l.Store.Instance):
		l.modOn = true
		l.Store.Event("mod_drives", nil)
		return modOn
	case h.Instance == l.Store.Instance || h.Refused != "":
		return modOff
	case v.Now.Sub(l.hostAt) < l.Timing.ModHello:
		return modWaiting
	}
	return modOff
}

// viaMod reports that baton's mod carries what the lead's loop gives the lead's claude.
func (l *Loop) viaMod() bool { return l.mode == modOn }

// bridge is the lead's run's side of the bridge to baton's mod, which logs to the run.
func (l *Loop) bridge() *bridge.Bridge {
	b := bridge.Open(l.Store.Dir, l.Store.Now, func(kind string, fields map[string]any) { l.Store.Event(kind, fields) })
	b.Drop = decide.ReportKept
	return b
}

// say gives the model text as a prompt of its own: submitted by baton's mod where it runs, otherwise typed,
// with Enter (a phase agent's loop types it on its line, a message).
func (l *Loop) say(v host.View, in host.Injector, text string) error {
	if l.viaMod() {
		return l.submit(v, text)
	}
	return in.Type(text, true)
}

// submit has the mod submit text to the lead's model (bridge.Submit). One line is open at a time: the next
// waits until the mod has acked it, and one not acked within modAckWait is taken back (bridge.Void) and the
// next issued in its place.
func (l *Loop) submit(v host.View, text string) error {
	b := l.bridge()
	if l.submitting != 0 {
		if l.open(b, l.submitting) {
			if v.Now.Sub(l.submittedAt) < modAckWait {
				return errSubmitOpen
			}
			b.Void(l.Store.Instance, l.submitting) // the mod never ran it
		}
		l.submitting = 0
	}
	cmd, err := b.Issue(bridge.Command{Kind: bridge.Submit, Instance: l.Store.Instance, Text: text})
	if err != nil {
		return err
	}
	l.submitting, l.submittedAt = cmd.ID, v.Now
	return nil
}

// open reports whether this host's command id is issued and not acked yet.
func (l *Loop) open(b *bridge.Bridge, id int) bool {
	rec, err := b.Peek()
	return err == nil && slices.ContainsFunc(rec.Open, func(o bridge.Issued) bool { return o.ID == id && o.Instance == l.Store.Instance })
}

// askCompact has the mod compact the lead (bridge.Compact) for the compaction queued, and records the try
// (Compaction.Command). The command names the compaction's epoch, so the mod's ack counts for it however
// soon it comes, before the try is recorded too: `baton mod report` moves it under way, or declines it.
func (l *Loop) askCompact(v host.View, st state.State) {
	comp := st.Run.Compaction
	cmd, err := l.bridge().Issue(bridge.Command{Kind: bridge.Compact, Instance: l.Store.Instance, Epoch: comp.Epoch})
	if err != nil {
		l.logf("loop: asking the mod to compact: %v", err)
		return
	}
	l.update(func(st *state.State) {
		c := &st.Run.Compaction
		if c.Epoch != comp.Epoch {
			return
		}
		c.Command = cmd.ID
		if c.Status == state.CompactQueued {
			c.Status, c.Typed, c.Tries = state.CompactTyped, v.Now, c.Tries+1
		}
	})
	l.Store.Event("compact_asked", map[string]any{"epoch": comp.Epoch, "try": comp.Tries + 1, "round": comp.Rounds + 1, "reason": comp.Reason, "command": cmd.ID})
}

// asked watches a compaction the mod was asked for and has not begun: declined, it is asked again (redo);
// not acked within modAckWait, the command is taken back and the compaction asked again; and with nothing
// said of it within CompactTimeout (an ack lost), it has failed.
func (l *Loop) asked(v host.View, st state.State) {
	comp := st.Run.Compaction
	since := v.Now.Sub(l.since(comp.Typed))
	switch {
	case comp.Refused() != "":
		l.redo(v, comp, comp.Refused())
	case since > modAckWait && l.open(l.bridge(), comp.Command):
		if found, err := l.bridge().Void(l.Store.Instance, comp.Command); err == nil && found {
			l.redo(v, comp, decide.ModSilent(modAckWait))
		}
	case since > l.Timing.CompactTimeout:
		l.fail(comp, decide.ModNoWord(l.Timing.CompactTimeout))
	}
}

// redo asks again for a compaction a try did not make (why): one Claude Code refused because a turn was
// running is asked again once the turn is over, as if it never had been; any other try counts, and after
// MaxTries the round has failed.
func (l *Loop) redo(v host.View, comp state.Compaction, why string) {
	busy := turnRunning(why)
	if !busy && comp.Tries >= MaxTries {
		l.fail(comp, decide.ModCompactFailed(comp.Tries, why))
		return
	}
	l.update(func(st *state.State) {
		c := &st.Run.Compaction
		if c.Epoch != comp.Epoch || !c.InFlight() {
			return
		}
		c.Status, c.Declined = state.CompactQueued, nil
		if busy && c.Tries > 0 {
			c.Tries--
		}
	})
	if busy {
		l.declinedAt = v.Now
	}
	l.Store.Event("compact_retry", map[string]any{"epoch": comp.Epoch, "tries": comp.Tries, "why": why, "turn_running": busy})
}

// turnRunning reports Claude Code's refusal of a compaction asked for while a turn runs (its words, spike
// 35: "a turn is running (<turnId>); the conversation compacts between turns").
func turnRunning(why string) bool { return strings.Contains(why, "a turn is running") }

// via says, for an event, how the loop gave the lead its line: "mod", or "keys".
func (l *Loop) via() string {
	if l.viaMod() {
		return "mod"
	}
	return "keys"
}

// unsaid logs that a line baton was giving the model (doing what) did not go out; it is given again at a
// later tick. One held back while the line before it is not acked yet is no news.
func (l *Loop) unsaid(err error, doing string) {
	if !errors.Is(err, errSubmitOpen) && !errors.Is(err, errSendOpen) {
		l.logf("loop: %s: %v", doing, err)
	}
}
