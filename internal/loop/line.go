package loop

import (
	"errors"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A phase agent has one way in: a message (bridge.Send), which Claude Code holds for the agent's next tool
// round, whether it runs a command, compacts or idles, and which starts an idle agent again (spike 32). Its
// loop (Agent) reaches it through a line as the lead's reaches the lead through the keyboard: the line
// that wakes it for its results or baton's messages, which its hooks then hand over, once each; the
// watchdog's reminders; a retry after an API error. One message to an agent is open at a time: the next
// waits until the mod has acked it, and one not acked within sendWait is taken back (bridge.Void), so that
// the loop sends again when it is due.

// sendWait is how long the mod has to ack a message to an agent before baton takes it back.
const sendWait = time.Minute

// errSendOpen holds a message to an agent back while the one before it is not acked yet.
var errSendOpen = errors.New("the message before it is not acked yet")

// line is the Injector of the loop of w's agent, in the run s.
type line struct {
	c *Coordinator
	s *state.Store
	w *worker
}

func (l line) Type(text string, _ bool) error { return l.c.send(l.s, l.w, text) }

// ClearInput does nothing: an agent has no input box of baton's to clear.
func (line) ClearInput() error { return nil }

// Quit does nothing: an agent is ended by the coordinator (end), never by its loop.
func (line) Quit() error { return nil }

// send gives text to w's agent, in a message of its own.
func (c *Coordinator) send(s *state.Store, w *worker, text string) error {
	now := s.Now()
	b := c.bridge(s)
	if w.sending != 0 {
		if now.Sub(w.sentAt) < sendWait {
			return errSendOpen
		}
		b.Void(s.Instance, w.sending) // the mod never ran it: what it said is sent again when it is due
		w.sending = 0
	}
	cmd, err := b.Issue(bridge.Command{Kind: bridge.Send, Instance: s.Instance, Phase: w.phase, Agent: c.agentOf(s, w), Text: text, Summary: "baton"})
	if err != nil {
		return err
	}
	w.sending, w.sentAt = cmd.ID, now
	return nil
}

// sendAcked deals with the mod's ack r of the message w's agent was sent. Claude Code refuses one (its
// words, spike 32) to an agent the human stopped from the agent panel, whose phase is then theirs (hold),
// and to one that is gone for good (after /clear), which a fresh agent replaces in its worktree (afresh).
func (c *Coordinator) sendAcked(s *state.Store, w *worker, r bridge.Report) {
	w.sending = 0
	if r.OK || r.Interrupted {
		return // an interrupted one may or may not have gone: the loop sends again while it is due
	}
	said := strings.TrimSpace(r.Error + " " + r.Result)
	c.Lead.logf("loop: %s: the message to its agent failed: %s", w.phase, said)
	w.store.Event("agent_message_failed", map[string]any{"error": said})
	switch {
	case strings.Contains(said, "stopped by the user"):
		c.holdAgent(s, c.bridge(s), w)
	case strings.Contains(said, "could not be resumed"):
		why := "its agent could not be started again: " + said
		c.setEntry(s, w.entry, func(e *state.Worker) {
			if e.Live() {
				e.Status, e.Since, e.Why = state.WorkerSuspended, s.Now(), why
			}
		})
	}
}
