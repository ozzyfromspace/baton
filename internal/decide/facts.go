package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The facts the run's phases record about the gates' environment (baton env): every later brief lists them,
// so that no phase finds the same thing again, and each phase agent at work is told of a new one.

// EnvFacts is what a brief says of the run's environment facts (baton env), oldest first: "" for none.
func EnvFacts(facts []state.Fact) string {
	if len(facts) == 0 {
		return ""
	}
	return "What this run's gates need of their environment, as its phases found it (baton env), oldest first:\n" + factList(facts) +
		"\nAllow for them in your gates: a lane has only what .baton-lane sets up. When you find another, record it with `baton env \"<what the environment needs>\"`."
}

// factList lists facts a line each, with the phase that found each.
func factList(facts []state.Fact) string {
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "- %s (%s)\n", f.Text, finder(f.Phase))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// finder names the phase that found a fact: the lead found one with no phase in hand.
func finder(phase string) string {
	if phase == "" {
		return "the lead"
	}
	return phase
}

// FactTold is the message that tells a phase agent at work of fact f, which another phase recorded.
func FactTold(f state.Fact) string {
	return fmt.Sprintf("[baton] %s found what this run's gates need of their environment, and recorded it (baton env): %s. "+
		"Allow for it in your gates: a lane has only what .baton-lane sets up.", finder(f.Phase), f.Text)
}

// FactRecorded tells the human that fact f is on record, and which phase agents at work were told of it.
func FactRecorded(f state.Fact, told []string) string {
	line := fmt.Sprintf("baton: %s recorded what the gates' environment needs — %s → every later brief lists it", finder(f.Phase), f.Text)
	if len(told) > 0 {
		line += "; " + andList(told) + " told"
	}
	return line
}

// FactSaid is what `baton env` says once fact f is on record as the run's n-th, told to the phase agents
// told.
func FactSaid(f state.Fact, n int, told []string) string {
	line := fmt.Sprintf("baton: recorded, as the run's environment fact %d: %s. Every later brief in this run lists it", n, f.Text)
	if len(told) > 0 {
		line += fmt.Sprintf(", and %s, at work now, %s told", andList(told), plural(len(told), "is", "are"))
	}
	return line + ". Make it permanent in .baton-lane too, so that every lane is set up with it."
}

// FactKnown is what `baton env` says of a fact the run has already, as f.
func FactKnown(f state.Fact) string {
	return fmt.Sprintf("baton: already on record, from %s: %s. Nothing new to record.", finder(f.Phase), f.Text)
}

// EnvUsage is `baton env`'s usage.
const EnvUsage = "usage: baton env \"<what the gates' environment needs>\" (a port to leave free, a service to start first): every later brief in this run lists it"

// FactTooLong turns down a fact of n bytes, past state.MaxFactLen.
func FactTooLong(n int) string {
	return fmt.Sprintf("not recorded — a fact is one line of what the gates' environment needs, at most %d characters (this one is %d): "+
		"say it shorter, and put the rest in .baton-lane or your notes.", state.MaxFactLen, n)
}

// FactsFull turns down a fact once the run holds state.MaxFacts.
func FactsFull() string {
	return fmt.Sprintf("not recorded — this run holds %d environment facts already, as many as its briefs list: "+
		"make what the gates need permanent in .baton-lane, which sets up every lane, instead.", state.MaxFacts)
}
