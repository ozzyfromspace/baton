package loop

import (
	"errors"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The human writes to a phase's agent from the lead's prompt: `@P23 <words>` (spike 40). The lead's
// UserPromptSubmit hook keeps the prompt from the lead's model and queues the words in the run's state
// (state.HumanWords) for the agent at work on the phase. The coordinator sends each to that agent through
// baton's mod, as a message of its own (line.go), which Claude Code holds for the agent's next tool round
// and which wakes an idle agent, framed as the human's own words (decide.HumanToAgent); the mod's ack takes
// them off the queue. Words no agent can take any more (its agent ended or was stopped first, or Claude
// Code refused the message) go to the lead's model instead, and the human is told. Claude Code keeps a
// blocked prompt in the lead's input box, where a second Enter would send it again, so a moment after the
// prompt the mod empties the box of it, if it still holds exactly that prompt (bridge.Empty).

// emptyAfter is how long after the human's prompt for an agent the mod empties the lead's input box of it:
// a blocked prompt is back in the box by then (spike 40 emptied it from a timer a second later).
const emptyAfter = time.Second

// toEmpty is a prompt the lead's input box is to be emptied of, written at at.
type toEmpty struct {
	prompt string
	at     time.Time
}

// toAgents looks over the human's words waiting for phases' agents in run s once: each is sent to the
// agent at work on its phase, unless it is on its way (the mod has not acked it yet, and sendWait has not
// passed), or goes to the lead's model when no agent of this host can take it. The input box is emptied
// of each prompt emptyAfter after it was written.
func (c *Coordinator) toAgents(now time.Time, s *state.Store, b *bridge.Bridge) {
	defer c.emptyBox(now, s, b)
	st, err := s.Load()
	if err != nil {
		return
	}
	for _, q := range st.ToAgents {
		c.boxed(q)
		w := c.workerOn(q.Phase)
		switch {
		case w != nil && w.words.Equal(q.At) && now.Sub(w.sentAt) < sendWait:
			// on its way: the mod's ack settles it
		case w == nil || w.agent.in == nil || w.ending != "":
			c.wordsToLead(now, s, q.At, decide.WordsGone)
		default:
			agent := c.agentOf(s, w)
			err := c.sendWith(s, w, decide.HumanToAgent(q.Phase, q.Text), decide.WordsSummary)
			switch {
			case errors.Is(err, errSendOpen):
				// after the message before it, once the mod has acked that
			case errors.Is(err, bridge.ErrUnknownAgent):
				c.wordsToLead(now, s, q.At, decide.WordsGone)
			case err != nil:
				c.Lead.logf("loop: %s: the human's words for its agent: %v", q.Phase, err) // sent at a later step
			default:
				w.words, w.wordsCmd = q.At, w.sending
				s.Event("human_to_agent", map[string]any{"phase": q.Phase, "agent": agent, "step": "sent", "command": w.sending})
			}
		}
	}
}

// workerOn is the phase agent this host started on phase, or nil.
func (c *Coordinator) workerOn(phase string) *worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.workers[phase]
}

// wordsAcked takes the mod's ack r of the message that gave w's agent the human's words: Claude Code took
// it (or the mod was reloaded as it ran it, which may or may not have sent it: it is not sent twice), and
// the words are off the queue; or it refused it, and they go to the lead's model.
func (c *Coordinator) wordsAcked(s *state.Store, w *worker, r bridge.Report) {
	at := w.words
	w.words, w.wordsCmd = time.Time{}, 0
	if !r.OK && !r.Interrupted {
		said := strings.TrimSpace(r.Error + " " + r.Result)
		s.Event("human_to_agent", map[string]any{"phase": w.phase, "agent": c.agentOf(s, w), "step": "acked", "ok": false, "error": said})
		c.wordsToLead(s.Now(), s, at, decide.WordsRefused(said))
		return
	}
	var took bool
	s.Update(func(st *state.State) error { _, took = state.TakeWords(st, at); return nil })
	if !took {
		return
	}
	fields := map[string]any{"phase": w.phase, "agent": c.agentOf(s, w), "step": "acked", "ok": true}
	if r.Interrupted {
		fields["interrupted"] = true
	}
	s.Event("human_to_agent", fields)
}

// wordsToLead gives the lead's model the human's words written at at, which no agent took (why), as a
// message of baton's (state.Tell, which the lead's next hook, or a line the host gives an idle lead,
// hands over), and tells the human.
func (c *Coordinator) wordsToLead(now time.Time, s *state.Store, at time.Time, why string) {
	var q state.HumanWords
	var took bool
	s.Update(func(st *state.State) error {
		if q, took = state.TakeWords(st, at); took {
			say, tell := decide.WordsUnsent(q.Phase, q.Text, why)
			state.Tell(st, state.Message{Kind: state.MessageWords, Text: tell}, now)
			st.Run.Announce = append(st.Run.Announce, say)
		}
		return nil
	})
	if took {
		c.Lead.logf("loop: %s: the human's words for its agent went to the lead: %s", q.Phase, why)
		s.Event("human_to_agent", map[string]any{"phase": q.Phase, "agent": q.Agent, "routed": "lead", "step": "unsent", "why": why})
	}
}

// boxed keeps the prompt of q for the lead's input box to be emptied of, once.
func (c *Coordinator) boxed(q state.HumanWords) {
	if q.Prompt == "" || c.emptied[q.At.UnixNano()] {
		return
	}
	if c.emptied == nil {
		c.emptied = map[int64]bool{}
	}
	c.emptied[q.At.UnixNano()] = true
	c.empties = append(c.empties, toEmpty{prompt: q.Prompt, at: q.At})
}

// emptyBox has the mod empty the lead's input box of each prompt kept for it (boxed) once emptyAfter has
// passed since it was written: the mod empties it only while it holds exactly that prompt.
func (c *Coordinator) emptyBox(now time.Time, s *state.Store, b *bridge.Bridge) {
	kept := c.empties[:0]
	for _, e := range c.empties {
		if now.Sub(e.at) < emptyAfter {
			kept = append(kept, e)
			continue
		}
		if _, err := b.Issue(bridge.Command{Kind: bridge.Empty, Instance: s.Instance, Text: e.prompt}); err != nil {
			c.Lead.logf("loop: emptying the input box: %v", err) // the human empties it, or sends it again: queued once
		}
	}
	c.empties = kept
	if len(c.empties) == 0 && len(c.emptied) > 64 {
		c.emptied = nil // the prompts long since emptied: none is queued again
	}
}
