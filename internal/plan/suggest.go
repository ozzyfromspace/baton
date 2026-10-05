package plan

import (
	"regexp"
	"strings"
)

// Suggest proposes a Spec from common phase notations in a plan document. It is a starting point the
// model reviews and edits, never the source of truth: Build is what validates the final spec.
//
// Recognized notations (one per line):
//
//	## P0 — Title / ### P23: Title / ## R1 - Title      heading with a short id
//	## Phase 3 — Title / ## Phase C: Title              heading with "Phase <n>"
//	- **P6 Title:** …                                   bold bullet starting with an id
//	| P4 | Title | …                                    table row starting with an id
//
// A document lists its phases in one notation, so only the strongest notation present counts: headings,
// then bold bullets, then table rows. Lines in the others that merely look like phases (a table of
// spike results whose rows start with S1, S2, … ahead of the phase headings) are not phases.
func Suggest(doc []byte) Spec {
	lines := strings.Split(string(doc), "\n")
	best := notationNone
	for _, raw := range lines {
		if id, _, _, n := matchPhase(strings.TrimRight(raw, " \t\r")); id != "" && n < best {
			best = n
		}
	}
	var spec Spec
	lastPhaseLine, lastPhaseLevel := -1, 0
	for i, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		if spec.Title == "" {
			if m := reTitle.FindStringSubmatch(line); m != nil {
				spec.Title = strings.TrimSpace(m[1])
				continue
			}
		}
		if spec.RulesAnchor == "" && reRules.MatchString(line) {
			spec.RulesAnchor = strings.TrimSpace(line)
			continue
		}
		id, title, level, notation := matchPhase(line)
		if id == "" || notation != best {
			if lastPhaseLine >= 0 && spec.EndAnchor == "" {
				if m := reHeading.FindStringSubmatch(line); m != nil && len(m[1]) <= lastPhaseLevel && reEnd.MatchString(m[2]) {
					spec.EndAnchor = strings.TrimSpace(line)
				}
			}
			continue
		}
		if spec.EndAnchor != "" {
			continue // phases after the end marker belong to some other list
		}
		spec.Phases = append(spec.Phases, Phase{ID: id, Title: title, Anchor: strings.TrimSpace(line)})
		lastPhaseLine, lastPhaseLevel = i, level
	}
	return spec
}

var (
	reTitle   = regexp.MustCompile(`^#\s+(.+)$`)
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	reRules   = regexp.MustCompile(`(?i)^#{2,6}\s+.*\bstanding rules\b`)
	reEnd     = regexp.MustCompile(`(?i)^(verification|acceptance|risks?|what this plan does not do|out of scope|appendix)\b`)

	idPart       = `([PRMS]\d+[a-z]?)`
	sep          = `\s*(?:—|–|-|:|\.)\s*`
	reHeadingID  = regexp.MustCompile(`^(#{2,6})\s+` + idPart + sep + `(.+)$`)
	reHeadingPh  = regexp.MustCompile(`(?i)^(#{2,6})\s+phase\s+(\d+|[A-Z])\b` + `(?:` + sep + `(.*))?$`)
	reBoldBullet = regexp.MustCompile(`^\s*[-*]\s+\*\*` + idPart + `\b\s*([^*:]*?)\s*:?\*\*`)
	reTableRow   = regexp.MustCompile(`^\|\s*\**` + idPart + `\**\s*\|\s*([^|]+?)\s*\|`)
)

// Phase notations, strongest first.
const (
	notationHeading = iota
	notationBullet
	notationTable
	notationNone
)

// matchPhase returns the id, title, heading level (7 for non-headings) and notation of a phase line, or
// an empty id.
func matchPhase(line string) (id, title string, level, notation int) {
	if m := reHeadingID.FindStringSubmatch(line); m != nil {
		return m[2], cleanTitle(m[3]), len(m[1]), notationHeading
	}
	if m := reHeadingPh.FindStringSubmatch(line); m != nil {
		t := cleanTitle(m[3])
		if t == "" {
			t = "Phase " + m[2]
		}
		return "Phase-" + m[2], t, len(m[1]), notationHeading
	}
	if m := reBoldBullet.FindStringSubmatch(line); m != nil {
		t := cleanTitle(m[2])
		if t == "" {
			t = m[1]
		}
		return m[1], t, 7, notationBullet
	}
	if m := reTableRow.FindStringSubmatch(line); m != nil {
		return m[1], cleanTitle(m[2]), 7, notationTable
	}
	return "", "", 0, notationNone
}

func cleanTitle(s string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "*_`"))
}
