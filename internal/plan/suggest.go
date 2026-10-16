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
//
// When the suggestion builds, each phase carries the dependencies the text gives it (After).
//
// Lines in fenced code blocks are passed over: a heading quoted in an example is not the plan's. Each
// anchor is made to occur once in the document (once), so a plan that quotes its own headings in its
// text ("`## Standing rules`") still attaches.
func Suggest(doc []byte) Spec {
	text := string(doc)
	lines := strings.Split(text, "\n")
	code, offs := fenced(lines), lineOffsets(lines)
	best := notationNone
	for i, raw := range lines {
		if id, _, _, n := matchPhase(strings.TrimRight(raw, " \t\r")); id != "" && n < best && !code[i] {
			best = n
		}
	}
	var spec Spec
	lastPhaseLine, lastPhaseLevel := -1, 0
	for i, raw := range lines {
		if code[i] {
			continue
		}
		line := strings.TrimRight(raw, " \t\r")
		if spec.Title == "" {
			if m := reTitle.FindStringSubmatch(line); m != nil {
				spec.Title = strings.TrimSpace(m[1])
				continue
			}
		}
		if spec.RulesAnchor == "" && reRules.MatchString(line) {
			spec.RulesAnchor = once(text, lines, offs, i)
			continue
		}
		id, title, level, notation := matchPhase(line)
		if id == "" || notation != best {
			if lastPhaseLine >= 0 && spec.EndAnchor == "" {
				if m := reHeading.FindStringSubmatch(line); m != nil && len(m[1]) <= lastPhaseLevel && reEnd.MatchString(m[2]) {
					spec.EndAnchor = once(text, lines, offs, i)
				}
			}
			continue
		}
		if spec.EndAnchor != "" {
			continue // phases after the end marker belong to some other list
		}
		spec.Phases = append(spec.Phases, Phase{ID: id, Title: title, Anchor: once(text, lines, offs, i)})
		lastPhaseLine, lastPhaseLevel = i, level
	}
	if p, err := Build("", doc, spec); err == nil {
		spec.Phases = p.Phases
	}
	return spec
}

// maxAnchorLines bounds how many lines after a heading once may add to make its anchor unique.
const maxAnchorLines = 8

// once returns the anchor for the heading on line i of text: the line itself when it occurs once in text;
// else the line with its line end, which a quote of it inside other text ("see `## Verification`") lacks;
// else the line and as many lines of its section after it as make it occur once (never into the next
// heading, so a heading written twice stays two). When nothing does, it is the line itself, and Build
// says it occurs more than once.
func once(text string, lines []string, offs []int, i int) string {
	line := strings.TrimSpace(lines[i])
	if strings.Count(text, line) <= 1 {
		return line
	}
	start := offs[i] + strings.Index(lines[i], line)
	if i+1 < len(lines) {
		if a := text[start:offs[i+1]]; strings.Count(text, a) == 1 {
			return a
		}
	}
	for j := i + 1; j < len(lines) && j <= i+maxAnchorLines; j++ {
		end := strings.TrimRight(lines[j], " \t\r")
		if reHeading.MatchString(end) {
			break
		}
		if end == "" {
			continue
		}
		if a := text[start : offs[j]+len(end)]; strings.Count(text, a) == 1 {
			return a
		}
	}
	return line
}

// lineOffsets returns where each of lines (text split at "\n") starts in text.
func lineOffsets(lines []string) []int {
	offs := make([]int, len(lines))
	at := 0
	for i, l := range lines {
		offs[i] = at
		at += len(l) + 1
	}
	return offs
}

// fenced marks the lines of a fenced code block (``` or ~~~, closed by a fence of the same character at
// least as long), its fences included.
func fenced(lines []string) []bool {
	in := make([]bool, len(lines))
	fence := ""
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		switch {
		case fence != "":
			in[i] = true
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = "" // the closing fence
			}
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
			in[i] = true
			fence = t[:len(t)-len(strings.TrimLeft(t, t[:1]))]
		}
	}
	return in
}

// heading is an anchor's first line: the heading it starts at.
func heading(anchor string) string {
	first, _, _ := strings.Cut(anchor, "\n")
	return strings.TrimSpace(first)
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
