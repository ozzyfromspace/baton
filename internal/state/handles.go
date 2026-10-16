package state

import (
	"errors"
	"os"
	"slices"
	"time"
)

// A phase's id is the name of baton's agent on it, and the human's handle on that agent (plan's names.go):
// `@P23 <words>` at the lead's prompt writes to it (spike 40). The lead's UserPromptSubmit hook reads where
// the phase stands (Addressee): words for an agent at work wait here (State.ToAgents), the prompt kept from
// the lead's model, until the host's coordinator has given them to the agent through baton's mod
// (loop/words.go); words for a phase with no agent at work go to the lead's model with the prompt.

// HumanWords are the human's words (Text) for the phase agent on Phase, which was Agent when they wrote
// them, in the prompt Prompt, as they typed it, at At, which names them.
type HumanWords struct {
	Phase  string    `json:"phase"`
	Agent  string    `json:"agent"`
	Text   string    `json:"text"`
	Prompt string    `json:"prompt"`
	At     time.Time `json:"at"`
}

// QueueWords queues w for its agent, and reports whether it did: the same words for the same phase still
// queued are the same prompt sent again (Claude Code keeps a blocked prompt in the input box, and a second
// Enter submits it again), and are queued once.
func QueueWords(st *State, w HumanWords) bool {
	if slices.ContainsFunc(st.ToAgents, func(o HumanWords) bool { return o.Phase == w.Phase && o.Text == w.Text }) {
		return false
	}
	st.ToAgents = append(st.ToAgents, w)
	return true
}

// TakeWords takes the words written at at off the queue, and reports whether they were on it.
func TakeWords(st *State, at time.Time) (HumanWords, bool) {
	i := slices.IndexFunc(st.ToAgents, func(w HumanWords) bool { return w.At.Equal(at) })
	if i < 0 {
		return HumanWords{}, false
	}
	w := st.ToAgents[i]
	st.ToAgents = slices.Delete(st.ToAgents, i, i+1)
	return w, true
}

// Where the human's words for a phase go, by where it stands (Addressee): to its agent at work (ToAgent),
// or, with no agent of baton's at work on it, to the lead's model, for why.
const (
	ToAgent    = "agent"    // a phase agent works on it, and baton reaches it
	ToPending  = "pending"  // it has not started
	ToDone     = "done"     // it is done
	ToLanded   = "landed"   // it is done, and its agent's work landed on the run's branch
	ToLead     = "lead"     // the lead works on it
	ToStopped  = "stopped"  // the human stopped its agent
	ToStarting = "starting" // its agent is starting
	ToReplaced = "replaced" // its agent ended, and a fresh one is to take it up
	ToLanding  = "landing"  // its agent finished it, and its work waits to land
	ToRetiring = "retiring" // its agent is being let go
	ToUnknown  = "unknown"  // its agent is none baton's mod has: a message would not be baton's to send
	ToNone     = "none"     // no agent of baton's is at work on it, and the lead is not either
)

// Addressee is where the human's words for phase go, in run st: ToAgent, with the agent, for a worker at
// work on it whose agent baton's mod has (known) and that nobody is retiring; otherwise why no agent takes
// them, and the lead's model does.
func Addressee(st State, phase string, known func(agent string) bool) (to, agent string) {
	if w := st.WorkerOn(phase); w != nil {
		switch {
		case !w.Retiring.IsZero():
			return ToRetiring, ""
		case w.Status == WorkerHeld:
			return ToStopped, ""
		case w.Status == WorkerSuspended:
			return ToReplaced, ""
		case w.Status == WorkerLanding:
			return ToLanding, ""
		case w.Status != WorkerWorking || w.AgentID == "":
			return ToStarting, ""
		case !known(w.AgentID):
			return ToUnknown, ""
		}
		return ToAgent, w.AgentID
	}
	ps := st.Phases[phase]
	switch {
	case ps != nil && ps.Status == PhaseDone && slices.ContainsFunc(st.Landed, func(l Landing) bool { return l.Phase == phase && l.By == "" }):
		return ToLanded, ""
	case ps != nil && ps.Status == PhaseDone:
		return ToDone, ""
	case phase == st.Current:
		return ToLead, ""
	case ps == nil || ps.Status == PhasePending:
		return ToPending, ""
	case ps.Status == PhaseWorking:
		return ToNone, "" // a worker of an earlier plan, or one being retired
	}
	return ToLead, "" // verifying or set aside, in the lead's checkout
}

// An agent baton let go of for good (its worker retired, or a fresh agent put in its place) may be woken
// all the same: by the human typing in its own view in Claude Code's agent panel, which nothing of baton's
// sees (spike 40). Where its worktree is gone, Claude Code resumes it in the lead's own checkout, and
// where it is not, beside the agent that took its place. So baton records each one it lets go of
// (LetGo), its hooks find it there (LetGoOf), and every tool call it makes is refused (hooks). The record
// is a link to the agent's sub-run, as an agent's own link is, under retired/, and goes with its run.
const retiredDir = "retired"

// wokeSuffix marks an agent let go of that has since woken: its waking is logged once.
const wokeSuffix = ".woke"

// LetGo records that baton let go of agentID, the phase agent of worker ref, for good.
func (p *Project) LetGo(ref, agentID string) error {
	if !validID(agentID) || !isWorker(ref) {
		return errors.New("cannot record agent " + agentID + " of " + ref + " as let go")
	}
	return p.withLock(func() error { return p.writeLink(ref, retiredDir, agentID) })
}

// LetGoOf returns the sub-run of the phase agent agentID if baton let go of it for good (LetGo), or "".
func (p *Project) LetGoOf(agentID string) string {
	if !validID(agentID) {
		return ""
	}
	if ref := p.readLink(retiredDir, agentID); isWorker(ref) && p.runExists(ref) {
		return ref
	}
	return ""
}

// Woke records that agentID, which baton let go of, has woken, and reports whether this is the first
// time it was recorded: its waking is logged once.
func (p *Project) Woke(agentID string) bool {
	ref := p.LetGoOf(agentID)
	if ref == "" {
		return false
	}
	f, err := os.OpenFile(p.path(retiredDir, agentID+wokeSuffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.WriteString(ref + "\n")
	return err == nil
}
