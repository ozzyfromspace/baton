package plan

import (
	"slices"
	"strings"
)

// A replan restructures the phases of a running plan that have not started (`baton replan`): the model
// edits a draft of the plan document, Redraft builds the plan the draft makes and checks it against what
// the replan may change, and Compare says what it changed. Phases that started or are done (frozen) never
// change: their ids, their text and the phases they wait for stay as they were. What else may change is
// the replan's Limits: the order and the After: lines of the phases not started always may; adding,
// rewording and dropping phases, and the plan's goal sections (the title and the text before the first
// phase, the standing rules, the end section), only as far as its kind allows.

// Limits is what a replan may change beyond the order and the After: lines of the phases not started.
type Limits struct {
	Add  bool // add phases (a split keeps its id on one part, and gives the others new ids)
	Edit bool // reword a phase not started: its text other than its After: line
	Drop bool // drop a phase not started (a merge drops the ids it absorbs)
	Goal bool // change the goal sections
}

// Goal sections of a plan document, as a replan compares them.
const (
	GoalPreamble = "preamble" // the title and the text before the first phase, its standing rules aside
	GoalRules    = "rules"    // the standing rules
	GoalEnd      = "end"      // the end section (## Verification), to the end of the document
)

var goalParts = []string{GoalPreamble, GoalRules, GoalEnd}

// Breach is one thing a replan's draft changed that it may not, or a draft that does not build.
type Breach struct {
	Kind   string   `json:"kind"`
	Phase  string   `json:"phase,omitempty"`  // the phase it concerns, or the goal part (BreachGoal)
	Detail string   `json:"detail,omitempty"` // why the draft does not build (BreachBuild)
	Was    []string `json:"was,omitempty"`    // BreachDeps: the phases it waited for
	Now    []string `json:"now,omitempty"`    // and the ones it would wait for
}

// Breach kinds.
const (
	BreachBuild   = "build"   // the draft does not build as a plan (Build)
	BreachGone    = "gone"    // a frozen phase is not in the draft
	BreachText    = "text"    // a frozen phase's text changed
	BreachDeps    = "deps"    // a frozen phase would wait for other phases
	BreachGoal    = "goal"    // a goal section changed
	BreachDropped = "dropped" // a phase not started is gone
	BreachAdded   = "added"   // a phase is new
	BreachEdited  = "edited"  // a phase not started was reworded
	BreachQA      = "qa"      // a QA phase lost its Type: qa line
)

// Change is one thing a replan changed, as Compare reads it.
type Change struct {
	Kind  string   `json:"kind"`
	Phase string   `json:"phase,omitempty"` // the phase, or the goal part (ChangeGoal)
	Was   []string `json:"was,omitempty"`   // ChangeAfter: the phases it waited for
	Now   []string `json:"now,omitempty"`   // ChangeAfter, ChangeAdded: the phases it waits for
}

// Change kinds.
const (
	ChangeAdded   = "added"
	ChangeDropped = "dropped"
	ChangeAfter   = "after" // it waits for other phases
	ChangeText    = "text"  // its text changed, its After: line aside
	ChangeMoved   = "moved" // it moved among the other phases
	ChangeGoal    = "goal"
)

// Part is p narrowed to the phases ids names, in p's order: each keeps only its dependencies on phases of
// the part, the others taken as done, so that Shape is the shape of the part alone (the phases a replan
// may restructure).
func (p Plan) Part(ids []string) Plan {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	q := p
	q.Phases = nil
	for _, ph := range p.Phases {
		if !keep[ph.ID] {
			continue
		}
		deps := []string{}
		for _, d := range p.Deps(ph.ID) {
			if keep[d] {
				deps = append(deps, d)
			}
		}
		ph.After = deps
		q.Phases = append(q.Phases, ph)
	}
	return q
}

// Rebuild builds the plan doc makes of p, the plan of an earlier text of the same document (was; nil when
// it is not at hand). The phases are read as baton read p's: a plan whose phases all start at headings is
// suggested afresh (Suggest), keeping p's title for each phase whose heading is unchanged; any other keeps
// the anchors of p that doc still has, with the phases doc adds (Added). The rules and end anchors are p's
// where doc keeps them, and the title p's unless doc's own title line changed.
func Rebuild(p Plan, was, doc []byte) (Plan, error) {
	text := string(doc)
	sug := Suggest(doc)
	spec := Spec{Title: p.Title}
	if Shaped(Spec{Phases: p.Phases}) && Shaped(sug) {
		spec.Phases = sug.Phases
		for i, ph := range spec.Phases {
			if j := p.Index(ph.ID); j >= 0 && heading(p.Phases[j].Anchor) == heading(ph.Anchor) {
				spec.Phases[i].Title = p.Phases[j].Title
			}
		}
	} else {
		for _, ph := range p.Phases {
			if strings.Count(text, ph.Anchor) == 1 {
				spec.Phases = append(spec.Phases, ph)
			}
		}
		spec.Phases = append(spec.Phases, Added(p, doc)...)
		inDocumentOrder(text, spec.Phases)
	}
	for i := range spec.Phases {
		spec.Phases[i].After, spec.Phases[i].Type, spec.Phases[i].Replan = nil, "", "" // the text says them
	}
	if was != nil && sug.Title != "" && sug.Title != Suggest(was).Title {
		spec.Title = sug.Title
	}
	spec.RulesAnchor = keptAnchor(text, p.RulesAnchor, sug.RulesAnchor)
	spec.EndAnchor = keptAnchor(text, p.EndAnchor, sug.EndAnchor)
	return Build(p.File, doc, spec)
}

// keptAnchor is anchor have where text still has it once, else the suggested one.
func keptAnchor(text, have, suggested string) string {
	if have != "" && strings.Count(text, have) == 1 {
		return have
	}
	return suggested
}

// Redraft builds the plan a replan's draft makes of old, whose text was was, and checks it: frozen phases
// (started or done) keep their ids, their text and the phases they wait for; a phase not started is
// dropped, added or reworded, and a goal section changed, only as lim allows; a QA phase stays one. The
// draft must build (Build: ids unique, anchors once and in order, every After: naming a phase before it,
// so the graph has no cycle). It returns the plan, and every breach, none when the draft may be followed.
func Redraft(old Plan, was, draft []byte, frozen map[string]bool, lim Limits) (Plan, []Breach) {
	p, err := Rebuild(old, was, draft)
	if err != nil {
		return Plan{}, []Breach{{Kind: BreachBuild, Detail: err.Error()}}
	}
	var out []Breach
	for i, ph := range old.Phases {
		j := p.Index(ph.ID)
		switch {
		case frozen[ph.ID] && j < 0:
			out = append(out, Breach{Kind: BreachGone, Phase: ph.ID})
		case frozen[ph.ID]:
			a, _ := old.Section(was, i)
			if b, _ := p.Section(draft, j); a != b {
				out = append(out, Breach{Kind: BreachText, Phase: ph.ID})
			}
			if before, now := old.Deps(ph.ID), p.Deps(ph.ID); !sameIDs(before, now) {
				out = append(out, Breach{Kind: BreachDeps, Phase: ph.ID, Was: before, Now: now})
			}
		case j < 0:
			if !lim.Drop {
				out = append(out, Breach{Kind: BreachDropped, Phase: ph.ID})
			}
		default:
			if ph.QA() && !p.Phases[j].QA() {
				out = append(out, Breach{Kind: BreachQA, Phase: ph.ID})
			}
			a, _ := old.Section(was, i)
			if b, _ := p.Section(draft, j); !lim.Edit && Body(a) != Body(b) {
				out = append(out, Breach{Kind: BreachEdited, Phase: ph.ID})
			}
		}
	}
	if !lim.Add {
		for _, ph := range p.Phases {
			if old.Index(ph.ID) < 0 {
				out = append(out, Breach{Kind: BreachAdded, Phase: ph.ID})
			}
		}
	}
	if !lim.Goal {
		og, ng := old.goal(was), p.goal(draft)
		for _, part := range goalParts {
			if og[part] != ng[part] {
				out = append(out, Breach{Kind: BreachGoal, Phase: part})
			}
		}
	}
	return p, out
}

// Compare lists what p, the plan of doc, changed of old, the plan of was: the goal sections first, then
// each phase in p's order (added, waiting for other phases, reworded, moved among the others), then the
// phases dropped. With was nil (old's text is not at hand), no text is compared: only the phases, what
// each waits for, and their order.
func Compare(old Plan, was []byte, p Plan, doc []byte) []Change {
	var out []Change
	if was != nil {
		og, ng := old.goal(was), p.goal(doc)
		for _, part := range goalParts {
			if og[part] != ng[part] {
				out = append(out, Change{Kind: ChangeGoal, Phase: part})
			}
		}
	}
	moved := movedPhases(old, p)
	for j, ph := range p.Phases {
		i := old.Index(ph.ID)
		if i < 0 {
			out = append(out, Change{Kind: ChangeAdded, Phase: ph.ID, Now: p.Deps(ph.ID)})
			continue
		}
		if before, now := old.Deps(ph.ID), p.Deps(ph.ID); !sameIDs(before, now) {
			out = append(out, Change{Kind: ChangeAfter, Phase: ph.ID, Was: before, Now: now})
		}
		if was != nil {
			a, _ := old.Section(was, i)
			if b, _ := p.Section(doc, j); Body(a) != Body(b) {
				out = append(out, Change{Kind: ChangeText, Phase: ph.ID})
			}
		}
		if moved[ph.ID] {
			out = append(out, Change{Kind: ChangeMoved, Phase: ph.ID})
		}
	}
	for _, ph := range old.Phases {
		if p.Index(ph.ID) < 0 {
			out = append(out, Change{Kind: ChangeDropped, Phase: ph.ID})
		}
	}
	return out
}

// movedPhases are the phases old and p share that moved among the others: all but the most of them that
// kept their order (a longest increasing run of their places in p).
func movedPhases(old, p Plan) map[string]bool {
	var common []string
	var at []int
	for _, ph := range old.Phases {
		if j := p.Index(ph.ID); j >= 0 {
			common, at = append(common, ph.ID), append(at, j)
		}
	}
	n := len(common)
	length, prev := make([]int, n), make([]int, n)
	best := -1
	for i := range n {
		length[i], prev[i] = 1, -1
		for k := range i {
			if at[k] < at[i] && length[k]+1 > length[i] {
				length[i], prev[i] = length[k]+1, k
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	kept := map[string]bool{}
	for i := best; i >= 0; i = prev[i] {
		kept[common[i]] = true
	}
	moved := map[string]bool{}
	for _, id := range common {
		if !kept[id] {
			moved[id] = true
		}
	}
	return moved
}

// Body is a phase's section without its After: line, which a change of structure edits, and with each
// run of blank lines read as one, so that adding or removing the line changes nothing else. The After:
// line is the one parseHead reads: among the lines that open the section, beside its Type: and Replan:
// lines.
func Body(section string) string {
	lines := strings.Split(section, "\n")
	seen := 0
	for k := 1; k < len(lines); k++ { // the first line is the phase's heading
		l := strings.TrimSpace(lines[k])
		if l == "" {
			continue
		}
		if seen++; seen > headLines {
			break
		}
		if a, _ := parseAfterLine(l); a.found {
			lines = slices.Delete(lines, k, k+1)
			break
		}
		_, typ, _ := parseTypeLine(l)
		_, replan, _ := parseReplanLine(l)
		if !typ && !replan {
			break // the section's text starts: an After: line below it is text
		}
	}
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			continue
		}
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// goal is p's goal sections in doc, by part (GoalPreamble, GoalRules, GoalEnd), each "" when doc has none.
func (p Plan) goal(doc []byte) map[string]string {
	text := string(doc)
	first := len(text)
	for _, ph := range p.Phases {
		if at, err := locate(text, ph.Anchor); err == nil && at < first {
			first = at
		}
	}
	pre, rules, end := text[:first], p.Rules(doc), ""
	if rules != "" {
		pre = strings.Replace(pre, rules, "", 1)
	}
	if p.EndAnchor != "" {
		if at, err := locate(text, p.EndAnchor); err == nil {
			end = text[at:]
		}
	}
	return map[string]string{GoalPreamble: strings.TrimSpace(pre), GoalRules: rules, GoalEnd: strings.TrimSpace(end)}
}
