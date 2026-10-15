// Package plan describes a multi-phase plan document and how baton finds each phase in it.
//
// Plan documents are free-form markdown: phases appear as headings, bold bullets, table rows, and more.
// Rather than parse markdown, baton asks the model to name each phase's *anchor*, a verbatim snippet
// that starts the phase's section, and checks deterministically that every anchor occurs exactly once
// and in document order. A phase's section runs from its anchor to the next anchor. Which phases a phase
// waits for comes from the plan text too (graph.go).
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// SchemaVersion is the plan.json format version.
const SchemaVersion = 1

// Phase is one unit of work between two compactions.
type Phase struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Anchor string `json:"anchor"`
	// After lists the phases this one waits for, in document order (Deps). nil is a plan.json from before
	// dependencies, read as the phase before it; empty waits for none.
	After []string `json:"after"`
	// Type is the kind of phase, from its section's Type: line: TypeQA, or "" for one that builds.
	Type string `json:"type,omitempty"`
}

// TypeQA marks a QA phase: where the plan runs its full test suites, lints and end-to-end checks, and fixes
// what the phases since the last one left incoherent. The phases between QA phases run focused checks
// only (config aggregate_qa), so the heavy ones run once for many phases.
const TypeQA = "qa"

// QA reports a QA phase.
func (ph Phase) QA() bool { return ph.Type == TypeQA }

// NextQA is the first QA phase after phase id in the plan, or nil: where the heavy checks of what id
// builds run.
func (p Plan) NextQA(id string) *Phase {
	if p.QAFor != "" && len(p.Phases) == 1 && p.Phases[0].ID == id {
		return &Phase{ID: p.QAFor, Title: p.QAForTitle, Type: TypeQA} // a narrowed plan (Only)
	}
	for i := p.Index(id) + 1; i > 0 && i < len(p.Phases); i++ {
		if p.Phases[i].QA() {
			return &p.Phases[i]
		}
	}
	return nil
}

// QAAhead names the QA phase where the heavy checks of what phase id builds run, "P5 (QA)", or "" when id
// is a QA phase itself, which runs them, or none is ahead of it.
func (p Plan) QAAhead(id string) string {
	if i := p.Index(id); i >= 0 && p.Phases[i].QA() {
		return ""
	}
	if qa := p.NextQA(id); qa != nil {
		return qa.ID + " (" + qa.Title + ")"
	}
	return ""
}

// SinceQA lists the phases a QA phase id checks: those between the QA phase before it (or the plan's
// start) and it.
func (p Plan) SinceQA(id string) []string {
	var out []string
	for i := p.Index(id) - 1; i >= 0 && !p.Phases[i].QA(); i-- {
		out = append([]string{p.Phases[i].ID}, out...)
	}
	return out
}

// Plan is the content of .baton/plan.json.
type Plan struct {
	Version int     `json:"version"`
	Title   string  `json:"title"`
	File    string  `json:"plan_file"`
	SHA256  string  `json:"plan_sha256"`
	Phases  []Phase `json:"phases"`
	// RulesAnchor optionally starts a "standing rules" section that is repeated in every brief.
	RulesAnchor string `json:"rules_anchor,omitempty"`
	// EndAnchor optionally ends the last phase's section (e.g. "## Verification").
	EndAnchor string `json:"end_anchor,omitempty"`
	// QAFor and QAForTitle name the QA phase that checks the one phase of a narrowed plan (Only), which
	// leaves it out (NextQA).
	QAFor      string `json:"qa_for,omitempty"`
	QAForTitle string `json:"qa_for_title,omitempty"`
	// Bounds are the anchors of the phases a narrowed plan leaves out (Only): sections still end at them.
	Bounds []string `json:"bounds,omitempty"`
}

// Spec is what the model supplies to `baton attach`: everything except the file identity.
type Spec struct {
	Title       string  `json:"title"`
	Phases      []Phase `json:"phases"`
	RulesAnchor string  `json:"rules_anchor,omitempty"`
	EndAnchor   string  `json:"end_anchor,omitempty"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// Build validates spec against the plan document's text and returns the plan to store, with each phase's
// dependencies derived from the text. Every error names the exact problem so the model can fix its spec
// (or the plan) and retry.
func Build(file string, doc []byte, spec Spec) (Plan, error) {
	text := string(doc)
	if errs := validate(text, spec); len(errs) > 0 {
		return Plan{}, errors.Join(errs...)
	}
	sum := sha256.Sum256(doc)
	p := Plan{
		Version: SchemaVersion, Title: strings.TrimSpace(spec.Title), File: file, SHA256: hex.EncodeToString(sum[:]),
		Phases: slices.Clone(spec.Phases), RulesAnchor: spec.RulesAnchor, EndAnchor: spec.EndAnchor,
	}
	errs := p.derive(text, func(int) bool { return true })
	for i, ph := range spec.Phases {
		if got := p.Phases[i].Type; ph.Type != "" && ph.Type != got {
			errs = append(errs, fmt.Errorf("phase %d (%q): the spec's \"type\" (%s) differs from the plan's (%q). A phase's type comes from the plan text, "+
				"a `Type: qa` line under its heading: edit the plan to change it, and leave \"type\" out of the spec", i+1, ph.ID, ph.Type, got))
		}
		if got := p.Phases[i].After; ph.After != nil && got != nil && !sameIDs(ph.After, got) {
			errs = append(errs, fmt.Errorf("phase %d (%q): the spec's \"after\" (%s) differs from the plan's (%s). Dependencies come from the plan text, "+
				"each phase's After: line (a phase without one follows the phase before it): edit the plan to change them, and leave \"after\" out of the spec",
				i+1, ph.ID, idList(ph.After), idList(got)))
		}
	}
	if len(errs) > 0 {
		return Plan{}, errors.Join(errs...)
	}
	return p, nil
}

// validate checks a spec's ids, titles and anchors against the plan text.
func validate(text string, spec Spec) []error {
	var errs []error
	if len(spec.Phases) == 0 {
		errs = append(errs, errors.New("the spec has no phases"))
	}
	seen := map[string]bool{}
	prev := -1
	for i, ph := range spec.Phases {
		where := fmt.Sprintf("phase %d (%q)", i+1, ph.ID)
		if !idPattern.MatchString(ph.ID) {
			errs = append(errs, fmt.Errorf("%s: id must be 1–32 letters, digits, '.', '_' or '-', starting with a letter or digit", where))
		}
		if seen[ph.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", where))
		}
		seen[ph.ID] = true
		if strings.TrimSpace(ph.Title) == "" {
			errs = append(errs, fmt.Errorf("%s: title is empty", where))
		}
		pos, err := locate(text, ph.Anchor)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		if pos <= prev {
			errs = append(errs, fmt.Errorf("%s: anchor appears before the previous phase's anchor; list phases in document order", where))
		}
		prev = pos
	}
	for _, f := range []struct{ name, anchor string }{{"rules_anchor", spec.RulesAnchor}, {"end_anchor", spec.EndAnchor}} {
		name, a := f.name, f.anchor
		if a == "" {
			continue
		}
		pos, err := locate(text, a)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		} else if name == "end_anchor" && pos <= prev {
			errs = append(errs, errors.New("end_anchor must come after the last phase's anchor"))
		}
	}
	return errs
}

func sameIDs(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func idList(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// locate returns the byte offset of anchor in text, requiring exactly one occurrence.
func locate(text, anchor string) (int, error) {
	if strings.TrimSpace(anchor) == "" {
		return 0, errors.New("anchor is empty")
	}
	switch n := strings.Count(text, anchor); n {
	case 0:
		return 0, fmt.Errorf("anchor %q does not occur in the plan document (it must be copied verbatim)", anchor)
	case 1:
		return strings.Index(text, anchor), nil
	default:
		return 0, fmt.Errorf("anchor %q occurs %d times; make it longer so it is unique", anchor, n)
	}
}

// Index returns the position of the phase with id, or -1.
func (p Plan) Index(id string) int {
	for i, ph := range p.Phases {
		if ph.ID == id {
			return i
		}
	}
	return -1
}

// Section returns the text of phase i in doc: from its anchor up to the nearest anchor after it (another
// phase's, the rules' or the end anchor), or the end of the document.
func (p Plan) Section(doc []byte, i int) (string, error) {
	sec, err := p.section(string(doc), i)
	if err != nil {
		return "", fmt.Errorf("phase %s: %w (was the plan edited after attach?)", p.Phases[i].ID, err)
	}
	return sec, nil
}

func (p Plan) section(text string, i int) (string, error) {
	start, err := locate(text, p.Phases[i].Anchor)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text[start:p.next(text, start)]), nil
}

// Rules returns the standing-rules section, or "" if the plan has none. It runs from the rules anchor
// to the next anchor of any kind after it.
func (p Plan) Rules(doc []byte) string {
	if p.RulesAnchor == "" {
		return ""
	}
	text := string(doc)
	start, err := locate(text, p.RulesAnchor)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(text[start:p.next(text, start)])
}

// next returns where a section starting at start ends: at the nearest anchor of any kind after it, or the
// end of text. Anchors that no longer occur exactly once are passed over.
func (p Plan) next(text string, start int) int {
	end := len(text)
	anchors := append([]string{p.RulesAnchor, p.EndAnchor}, p.Bounds...)
	for _, ph := range p.Phases {
		anchors = append(anchors, ph.Anchor)
	}
	for _, a := range anchors {
		if a == "" {
			continue
		}
		if e, err := locate(text, a); err == nil && e > start && e < end {
			end = e
		}
	}
	return end
}

// Only is p narrowed to phase id: the plan of a worker's sub-run, which holds only the phase it works on.
// The phase waits for nothing there (what it needed is done before a worker takes it up). Its section,
// the standing rules and the plan file are p's, so its brief reads as it would in p.
func (p Plan) Only(id string) (Plan, error) {
	i := p.Index(id)
	if i < 0 {
		return Plan{}, fmt.Errorf("unknown phase %q", id)
	}
	ph := p.Phases[i]
	ph.After = []string{}
	q := p
	q.Phases, q.Bounds = []Phase{ph}, slices.Clone(p.Bounds)
	q.QAFor, q.QAForTitle = "", ""
	if qa := p.NextQA(id); qa != nil {
		q.QAFor, q.QAForTitle = qa.ID, qa.Title
	}
	for _, o := range p.Phases {
		if o.ID != id {
			q.Bounds = append(q.Bounds, o.Anchor)
		}
	}
	return q, nil
}

// Load reads plan.json.
func Load(path string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	if p.Version != SchemaVersion {
		return p, fmt.Errorf("%s: unsupported plan version %d", path, p.Version)
	}
	return p, nil
}

// Shaped reports whether a suggested spec is a plan baton can attach without help: at least two phases,
// each starting at a heading of its own (## P0 — Title). baton attaches such a plan the moment the human
// approves it; anything else needs the model to check the spec (`baton attach --suggest`).
func Shaped(spec Spec) bool {
	if len(spec.Phases) < 2 {
		return false
	}
	for _, ph := range spec.Phases {
		if !strings.HasPrefix(ph.Anchor, "#") {
			return false
		}
	}
	return true
}

// Recheck reads the plan document again and compares it with the text baton attached. changed is false
// when it is that text. When it changed, err says what no longer fits: a phase still to run (one of
// remaining), the rules or the end anchor missing, duplicated or out of order, a dependency it cannot
// follow, or the file gone. With err nil, adopted is p with the new text's hash, and baton follows the new
// text: the phases it adds (Added) join the run, every phase stays in document order, and the
// dependencies of the phases still to run and the added ones are derived again from the new text.
func Recheck(p Plan, remaining []string) (adopted Plan, changed bool, err error) {
	doc, err := os.ReadFile(p.File)
	if err != nil {
		return p, true, fmt.Errorf("the plan file cannot be read: %w", err)
	}
	sum := sha256.Sum256(doc)
	if hex.EncodeToString(sum[:]) == p.SHA256 {
		return p, false, nil
	}
	text := string(doc)
	redo := map[string]bool{}
	for _, id := range remaining {
		redo[id] = true
	}
	added := Added(p, doc)
	for _, ph := range added {
		redo[ph.ID] = true
	}
	phases := append(slices.Clone(p.Phases), added...)
	inDocumentOrder(text, phases)
	spec := Spec{Title: p.Title, RulesAnchor: p.RulesAnchor, EndAnchor: p.EndAnchor}
	for _, ph := range phases {
		if redo[ph.ID] {
			spec.Phases = append(spec.Phases, ph)
		}
	}
	adopted = p
	adopted.SHA256, adopted.Phases = hex.EncodeToString(sum[:]), phases
	if len(spec.Phases) == 0 {
		return adopted, true, nil
	}
	if len(added) > 0 {
		// A phase added after the end marker (## Verification) is the last one now, and runs to the end.
		last := strings.Index(text, spec.Phases[len(spec.Phases)-1].Anchor)
		if spec.EndAnchor != "" && strings.Index(text, spec.EndAnchor) < last {
			spec.EndAnchor = ""
		}
	}
	if errs := validate(text, spec); len(errs) > 0 {
		return p, true, errors.Join(errs...)
	}
	adopted.EndAnchor = spec.EndAnchor
	if errs := adopted.derive(text, func(i int) bool { return redo[phases[i].ID] }); len(errs) > 0 {
		return p, true, errors.Join(errs...)
	}
	return adopted, true, nil
}

// Added returns the phases doc has that p lacks, in document order: headings in the notation and at the
// level of p's own phase headings (## P3 — Title), with ids p does not have. It is how a phase the human
// adds to the plan file joins the run. A plan whose phases are not all such headings gets none: its
// phases were named by the model, and a line that merely looks like a phase is not one.
func Added(p Plan, doc []byte) []Phase {
	level := 0
	have := map[string]bool{}
	for _, ph := range p.Phases {
		_, _, l, n := matchPhase(ph.Anchor)
		if n != notationHeading || level != 0 && l != level {
			return nil
		}
		level, have[ph.ID] = l, true
	}
	var out []Phase
	for _, raw := range strings.Split(string(doc), "\n") {
		line := strings.TrimSpace(raw)
		id, title, l, n := matchPhase(line)
		if id == "" || n != notationHeading || l != level || have[id] {
			continue
		}
		have[id] = true
		out = append(out, Phase{ID: id, Title: title, Anchor: line})
	}
	return out
}

// SHA reports the hash of the plan document as it is now, or "" if it cannot be read.
func (p Plan) SHA() string {
	doc, err := os.ReadFile(p.File)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}
