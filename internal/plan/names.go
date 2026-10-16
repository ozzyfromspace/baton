package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A phase's id is the name of its agent: baton's mod starts each phase agent with the Agent tool, named
// for its phase, and the human reaches the agent by that name (@P3 at the lead's prompt). So every id is
// a name Claude Code's Agent tool accepts (2.1.301): letters, digits, '_' and '-', starting with a letter
// or digit (Claude Code takes 64 characters, baton 32), none of the names Claude Code keeps for itself
// ("main", "team-lead", "user" and "system", in any case), and nothing of the shape of an agent id ("a",
// an optional prefix ending in '-', then 16 hex digits), since those address an agent already. Nor are
// two ids of a plan equal ignoring case: SendMessage falls back to a match ignoring case. The skill and
// Suggest write P<n> (P8b for a split), and Phase-A and the other notations stay valid.

var (
	idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	// agentIDShape is Claude Code's agent id, read of a name in lower case, as Claude Code reads it.
	agentIDShape  = regexp.MustCompile(`^a(?:[a-z0-9_-]{1,63}-)?[0-9a-f]{16}$`)
	reservedNames = []string{"main", "team-lead", "user", "system"}
)

// idProblem is why id cannot name a phase, and so its agent, or "" when it can.
func idProblem(id string) string {
	if !idPattern.MatchString(id) {
		return "id must be 1–32 letters, digits, '_' or '-' (no '.'), starting with a letter or digit: it is the name of the phase's agent, and Claude Code takes no other"
	}
	low := strings.ToLower(id)
	if slices.Contains(reservedNames, low) {
		return "id is a name Claude Code keeps for itself (main, team-lead, user and system, in any case), and it would name the phase's agent: give the phase another id, such as P1"
	}
	if agentIDShape.MatchString(low) {
		return "id has the shape of a Claude Code agent id (a, then 16 hex digits), which Claude Code refuses as an agent's name: give the phase another id, such as P1"
	}
	return ""
}

// checkIDs checks the ids of phases, in plan order: each names a phase's agent (idProblem), and no two
// are equal, even ignoring case. An id of kept passes as it is, and so do two of kept equal ignoring
// case: a plan baton runs had them before it checked names (Rebuild, Follow).
func checkIDs(phases []Phase, kept map[string]bool) []error {
	var errs []error
	seen := map[string]string{} // lower case → the id first seen
	for i, ph := range phases {
		where := fmt.Sprintf("phase %d (%q)", i+1, ph.ID)
		if why := idProblem(ph.ID); why != "" && !kept[ph.ID] {
			errs = append(errs, fmt.Errorf("%s: %s", where, why))
		}
		key := strings.ToLower(ph.ID)
		first, dup := seen[key]
		switch {
		case !dup:
			seen[key] = ph.ID
		case first == ph.ID:
			errs = append(errs, fmt.Errorf("%s: duplicate id", where))
		case !kept[first] || !kept[ph.ID]:
			errs = append(errs, fmt.Errorf("%s: id equals %s's ignoring case, and Claude Code tells its agents' names apart ignoring case: give each phase an id of its own", where, first))
		}
	}
	return errs
}

// idSet is the ids of p's phases.
func idSet(p Plan) map[string]bool {
	out := make(map[string]bool, len(p.Phases))
	for _, ph := range p.Phases {
		out[ph.ID] = true
	}
	return out
}

// Named is the id of p's phase whose id is name, ignoring case (as Claude Code matches an agent's name),
// or "" for none: the phase whose agent name addresses.
func (p Plan) Named(name string) string {
	name = strings.TrimSpace(name)
	for _, ph := range p.Phases {
		if strings.EqualFold(ph.ID, name) {
			return ph.ID
		}
	}
	return ""
}
