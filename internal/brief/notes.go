package brief

import "strings"

// LatestNotes is the latest notes phase left (handoff.md: its done notes, or a checkpoint's), without
// their heading; "" for none.
func LatestNotes(handoff, phase string) string {
	sec := notesOf(handoff, []string{phase})
	if sec == "" {
		return ""
	}
	_, body, _ := strings.Cut(sec, "\n")
	return strings.TrimSpace(body)
}
