package plan

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// The dependency graph says which phases a phase needs done before it starts. A phase names them on the
// first non-empty line under its heading:
//
//	After: P1, P3      the phases it builds on
//	After: none        it needs nothing another phase produces
//	After: all         every phase before it (integration and release phases)
//
// A phase without the line follows the phase before it, so a plan with no After: lines runs in order, as
// plans always have. Only earlier phases may be named, so the graph has no cycles. The plan text is the
// one source of the graph: Build and Recheck derive it, and a spec can only repeat it.

// after is a phase's After: line as written.
type after struct {
	found bool     // the section has an After: line
	all   bool     // After: all
	ids   []string // the phases it names (none when found, not all, and empty)
}

// parseAfter reads the After: line of a phase's section: one of the first non-empty lines after the
// phase's own heading, beside its Type: line. It accepts `After:` and `**After:**`, ids separated by
// commas, spaces or "and", `none` and `all`.
func parseAfter(section string) (after, error) {
	a, _, err := parseHead(section)
	return a, err
}

// parseHead reads the lines that open a phase's section, right under its heading, in either order: its
// After: line and its Type: line (`Type: qa` for a QA phase).
func parseHead(section string) (a after, typ string, err error) {
	lines := strings.Split(section, "\n")
	seen := 0
	for _, l := range lines[1:] { // the first line is the phase's own heading
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if seen++; seen > 2 {
			break
		}
		if t, ok, terr := parseTypeLine(l); ok {
			if terr != nil {
				return a, "", terr
			}
			typ = t
			continue
		}
		if a.found {
			break
		}
		if a, err = parseAfterLine(l); err != nil || !a.found {
			break
		}
	}
	return a, typ, err
}

// parseTypeLine reads a `Type:` line (`**Type:**` too): ok says it is one.
func parseTypeLine(line string) (typ string, ok bool, err error) {
	label, rest, found := strings.Cut(line, ":")
	if !found || !strings.EqualFold(strings.Trim(label, "*_` \t"), "type") {
		return "", false, nil
	}
	switch v := strings.ToLower(strings.TrimSuffix(strings.Trim(rest, "*` \t"), ".")); v {
	case TypeQA, "qa phase":
		return TypeQA, true, nil
	default:
		return "", true, fmt.Errorf("Type: %q is not a kind of phase baton knows: write `Type: qa` for a QA phase, or leave the line out", strings.TrimSpace(rest))
	}
}

func parseAfterLine(line string) (after, error) {
	label, rest, ok := strings.Cut(line, ":")
	if !ok || !strings.EqualFold(strings.Trim(label, "*_` \t"), "after") {
		return after{}, nil
	}
	rest = strings.TrimSuffix(strings.Trim(rest, "*` \t"), ".")
	var ids []string
	for _, tok := range strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == '&' || unicode.IsSpace(r) }) {
		if tok = strings.Trim(tok, "*`"); tok != "" && !strings.EqualFold(tok, "and") {
			ids = append(ids, tok)
		}
	}
	a := after{found: true}
	for _, id := range ids {
		if word := strings.ToLower(id); word == "none" || word == "all" {
			if len(ids) > 1 {
				return a, fmt.Errorf("After: %s stands alone; list phase ids, or write only `After: %s`", word, word)
			}
			a.all = word == "all"
			return a, nil
		}
	}
	if len(ids) == 0 {
		return a, errors.New("After: names no phase; write `After: none` for a phase that needs no other")
	}
	a.ids = ids
	return a, nil
}

// derive sets the After of each phase redo picks from text: the phases its After: line names, every
// phase before it for `all`, or the phase before it when it has no After: line. The other phases keep
// theirs. Ids are listed in document order.
func (p *Plan) derive(text string, redo func(i int) bool) []error {
	index := make(map[string]int, len(p.Phases))
	for i, ph := range p.Phases {
		index[ph.ID] = i
	}
	var errs []error
	for i := range p.Phases {
		if !redo(i) {
			continue
		}
		ph := &p.Phases[i]
		ph.After, ph.Type = nil, ""
		where := fmt.Sprintf("phase %d (%q)", i+1, ph.ID)
		sec, err := p.section(text, i)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		a, typ, err := parseHead(sec)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		ph.Type = typ
		var at []int
		switch {
		case !a.found && i > 0:
			at = []int{i - 1}
		case a.all:
			for j := range i {
				at = append(at, j)
			}
		}
		bad := false
		for _, id := range a.ids {
			j, ok := index[id]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%s: After: names %q, which is not a phase of this plan", where, id))
			case j == i:
				errs = append(errs, fmt.Errorf("%s: After: names the phase itself", where))
			case j > i:
				errs = append(errs, fmt.Errorf("%s: After: names %s, which comes after it; a phase can only wait for phases before it", where, id))
			default:
				at = append(at, j)
				continue
			}
			bad = true
		}
		if bad {
			continue
		}
		slices.Sort(at)
		ph.After = []string{}
		for _, j := range slices.Compact(at) {
			ph.After = append(ph.After, p.Phases[j].ID)
		}
	}
	return errs
}

// inDocumentOrder sorts phases by where their anchors are in text. A phase whose anchor is gone (a done
// phase the human renamed) stays just after the phase it followed.
func inDocumentOrder(text string, phases []Phase) {
	pos := make(map[string]int, len(phases))
	last := -1
	for _, ph := range phases {
		if at, err := locate(text, ph.Anchor); err == nil {
			last = at
		}
		pos[ph.ID] = last
	}
	sort.SliceStable(phases, func(i, j int) bool { return pos[phases[i].ID] < pos[phases[j].ID] })
}

// Deps returns the phases phase id waits for, in document order. A phase stored without After (a
// plan.json from before dependencies) waits for the phase before it.
func (p Plan) Deps(id string) []string {
	i := p.Index(id)
	switch {
	case i < 0:
		return nil
	case p.Phases[i].After != nil:
		return p.Phases[i].After
	case i == 0:
		return []string{}
	}
	return []string{p.Phases[i-1].ID}
}

// Ancestors returns every phase phase id waits for, directly or through another, in document order:
// everything done before it can start, which it may build on.
func (p Plan) Ancestors(id string) []string {
	seen := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		for _, d := range p.Deps(id) {
			if !seen[d] {
				seen[d] = true
				walk(d)
			}
		}
	}
	walk(id)
	var out []string
	for _, ph := range p.Phases {
		if seen[ph.ID] {
			out = append(out, ph.ID)
		}
	}
	return out
}

// Descendants returns every phase that waits for phase id, directly or through another, in document
// order: everything that cannot start until id is done.
func (p Plan) Descendants(id string) []string {
	var out []string
	for _, ph := range p.Phases {
		if slices.Contains(p.Ancestors(ph.ID), id) {
			out = append(out, ph.ID)
		}
	}
	return out
}

// Dependents returns the phases that wait for phase id, in document order.
func (p Plan) Dependents(id string) []string {
	var out []string
	for _, ph := range p.Phases {
		if slices.Contains(p.Deps(ph.ID), id) {
			out = append(out, ph.ID)
		}
	}
	return out
}

// LongestChain returns the length of the longest chain of phases that must run one after another, and
// the chain itself (the earliest such chain on a tie). It bounds how much of the plan can overlap.
func (p Plan) LongestChain() (k int, chain []string) {
	n := len(p.Phases)
	length, via := make([]int, n), make([]int, n)
	best := -1
	for i, ph := range p.Phases {
		length[i], via[i] = 1, -1
		for _, d := range p.Deps(ph.ID) {
			if j := p.Index(d); j >= 0 && j < i && length[j]+1 > length[i] {
				length[i], via[i] = length[j]+1, j
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	for i := best; i >= 0; i = via[i] {
		chain = append(chain, p.Phases[i].ID)
	}
	slices.Reverse(chain)
	return len(chain), chain
}

// Width is the most phases of the plan that can be worked on at once: the largest set of phases none of
// which waits for another, directly or through others. By Dilworth's theorem it is the number of phases
// less the largest matching of each phase to one that waits for it. A chain has a width of 1.
func (p Plan) Width() int {
	n := len(p.Phases)
	before := make([][]bool, n) // before[i][j]: phase i is done before phase j starts
	for i := range before {
		before[i] = make([]bool, n)
	}
	for j, ph := range p.Phases {
		for _, a := range p.Ancestors(ph.ID) {
			if i := p.Index(a); i >= 0 {
				before[i][j] = true
			}
		}
	}
	match := make([]int, n) // the phase matched to each phase that waits for it, or -1
	for j := range match {
		match[j] = -1
	}
	var augment func(i int, seen []bool) bool
	augment = func(i int, seen []bool) bool {
		for j := range n {
			if before[i][j] && !seen[j] {
				seen[j] = true
				if match[j] < 0 || augment(match[j], seen) {
					match[j] = i
					return true
				}
			}
		}
		return false
	}
	width := n
	for i := range n {
		if augment(i, make([]bool, n)) {
			width--
		}
	}
	return width
}

// Steps groups the plan's phases by how far down the graph they are: a phase that waits for none is in
// the first step, and any other in the step after the latest of the phases it waits for. So no phase of a
// step waits for another of it, and a step's phases may run at once once the steps before it are done.
// Each step lists its phases in document order.
func (p Plan) Steps() [][]string {
	step := make(map[string]int, len(p.Phases))
	var out [][]string
	for _, ph := range p.Phases {
		n := 0
		for _, d := range p.Deps(ph.ID) {
			if k, ok := step[d]; ok && k+1 > n {
				n = k + 1
			}
		}
		step[ph.ID] = n
		for len(out) <= n {
			out = append(out, nil)
		}
		out[n] = append(out[n], ph.ID)
	}
	return out
}

// Sequential reports whether each phase waits for exactly the phase before it, as every plan did before
// dependencies.
func (p Plan) Sequential() bool {
	for i, ph := range p.Phases {
		d := p.Deps(ph.ID)
		if i == 0 && len(d) != 0 || i > 0 && (len(d) != 1 || d[0] != p.Phases[i-1].ID) {
			return false
		}
	}
	return true
}

// DepsLabel says what phase id waits for, as the human reads it: "after P2 and P4", "needs no other
// phase", or "after every phase before it".
func (p Plan) DepsLabel(id string) string {
	d := p.Deps(id)
	switch i := p.Index(id); {
	case len(d) == 0:
		return "needs no other phase"
	case i >= 2 && len(d) == i:
		return "after every phase before it"
	}
	return "after " + andList(d)
}

// Dependencies describes the graph in one line: "dependencies: P0, P1 and P3 need no other phase; P5
// after P2 and P4; every other phase follows the one before it; longest chain 4 of 6 phases".
func (p Plan) Dependencies() string {
	if p.Sequential() {
		return "dependencies: each phase follows the one before it"
	}
	var roots, parts []string
	follows := false
	for i, ph := range p.Phases {
		d := p.Deps(ph.ID)
		switch {
		case len(d) == 0:
			roots = append(roots, ph.ID)
		case i > 0 && len(d) == 1 && d[0] == p.Phases[i-1].ID:
			follows = true
		default:
			parts = append(parts, ph.ID+" "+p.DepsLabel(ph.ID))
		}
	}
	if len(roots) > 1 {
		parts = append([]string{andList(roots) + " need no other phase"}, parts...)
	}
	if follows {
		parts = append(parts, "every other phase follows the one before it")
	}
	k, _ := p.LongestChain()
	return fmt.Sprintf("dependencies: %s; longest chain %d of %d phases", strings.Join(parts, "; "), k, len(p.Phases))
}

// andList joins ids as "P1", "P1 and P2", "P1, P2 and P3".
func andList(ids []string) string {
	if len(ids) < 2 {
		return strings.Join(ids, "")
	}
	return strings.Join(ids[:len(ids)-1], ", ") + " and " + ids[len(ids)-1]
}
