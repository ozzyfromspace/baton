// Package plan describes a multi-phase plan document and how baton finds each phase in it.
//
// Plan documents are free-form markdown: phases appear as headings, bold bullets, table rows, and more.
// Rather than parse markdown, baton asks the model to name each phase's *anchor*, a verbatim snippet
// that starts the phase's section, and checks deterministically that every anchor occurs exactly once
// and in document order. A phase's section runs from its anchor to the next anchor.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// SchemaVersion is the plan.json format version.
const SchemaVersion = 1

// Phase is one unit of work between two compactions.
type Phase struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Anchor string `json:"anchor"`
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
}

// Spec is what the model supplies to `baton attach`: everything except the file identity.
type Spec struct {
	Title       string  `json:"title"`
	Phases      []Phase `json:"phases"`
	RulesAnchor string  `json:"rules_anchor,omitempty"`
	EndAnchor   string  `json:"end_anchor,omitempty"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// Build validates spec against the plan document's text and returns the plan to store.
// Every error names the exact problem so the model can fix its spec and retry.
func Build(file string, doc []byte, spec Spec) (Plan, error) {
	text := string(doc)
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
	if len(errs) > 0 {
		return Plan{}, errors.Join(errs...)
	}
	sum := sha256.Sum256(doc)
	return Plan{
		Version: SchemaVersion, Title: strings.TrimSpace(spec.Title), File: file, SHA256: hex.EncodeToString(sum[:]),
		Phases: spec.Phases, RulesAnchor: spec.RulesAnchor, EndAnchor: spec.EndAnchor,
	}, nil
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

// Section returns the text of phase i in doc: from its anchor up to the next phase's anchor, the
// end anchor, or the end of the document.
func (p Plan) Section(doc []byte, i int) (string, error) {
	text := string(doc)
	start, err := locate(text, p.Phases[i].Anchor)
	if err != nil {
		return "", fmt.Errorf("phase %s: %w (was the plan edited after attach?)", p.Phases[i].ID, err)
	}
	end := len(text)
	if i+1 < len(p.Phases) {
		if e, err := locate(text, p.Phases[i+1].Anchor); err == nil {
			end = e
		}
	} else if p.EndAnchor != "" {
		if e, err := locate(text, p.EndAnchor); err == nil && e > start {
			end = e
		}
	}
	return strings.TrimSpace(text[start:end]), nil
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
	end := len(text)
	anchors := []string{p.EndAnchor}
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
	return strings.TrimSpace(text[start:end])
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
