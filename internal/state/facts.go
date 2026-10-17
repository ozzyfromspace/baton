package state

import (
	"strings"
	"time"
)

// Fact is one thing the gates' environment needs, as a model found it while it ran the plan (baton env):
// a port to leave free, a service to start first. Every later brief lists the run's facts, so no phase finds
// the same thing again.
type Fact struct {
	Text  string    `json:"text"`
	Phase string    `json:"phase,omitempty"` // the phase that found it
	At    time.Time `json:"at"`
}

// The facts are the run's, kept in its own state (a worker's sub-run keeps none: Store.Facts reads its
// run's). Each is a line, and a run keeps a few: what more there is to say belongs in .baton-lane, which
// sets every lane up.
const (
	MaxFactLen = 300 // characters of one fact
	MaxFacts   = 30  // facts of a run
)

// MessageFact is the kind of message that tells a phase agent at work of a fact another phase recorded
// (baton env): once, by the mail path. A fresh agent's brief lists the facts, so it drops these.
const MessageFact = "fact"

// KnownFact is the fact of st that says text already, the same words whatever their case and spacing, and
// whether there is one.
func KnownFact(st State, text string) (Fact, bool) {
	key := factKey(text)
	for _, f := range st.Facts {
		if factKey(f.Text) == key {
			return f, true
		}
	}
	return Fact{}, false
}

// AddFact records text as a fact phase found, at now, unless st has it already (KnownFact). It returns the
// fact on record, and whether it is new.
func AddFact(st *State, text, phase string, now time.Time) (Fact, bool) {
	if f, ok := KnownFact(*st, text); ok {
		return f, false
	}
	f := Fact{Text: text, Phase: phase, At: now}
	st.Facts = append(st.Facts, f)
	return f, true
}

func factKey(text string) string { return strings.ToLower(strings.Join(strings.Fields(text), " ")) }

// Facts is the environment facts of s's run, oldest first: st's own, or, for a worker's sub-run, those of
// the run it works for, read without its lock (so an Update of s may call it).
func (s *Store) Facts(st State) []Fact {
	if s.Parent == "" {
		return st.Facts
	}
	up, err := s.ParentRun()
	if err != nil {
		return nil
	}
	run, err := up.Peek()
	if err != nil {
		return nil
	}
	return run.Facts
}
