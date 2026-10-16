package transcript

// Claude Code writes a session's plan in plan mode to one file, <plansDirectory>/<slug>.md, and once that
// file exists it attaches the whole of it to the conversation after every compaction (an attachment entry
// of type plan_file_reference), whatever else the context holds; only the file's absence stops it
// (spike 38). The slug is the session's, written on its entries ("slug"), and can change over a session's
// life (a resume, say): the latest one is the session's now.

// Slug is the plan slug of the session whose transcript is at path, as the latest entry of its own
// conversation that carries one says (a subagent's entries are not its own): the session's plan-mode file
// is <plansDirectory>/<slug>.md. "" when the end of the transcript names none.
func Slug(path string) (string, error) {
	for _, n := range []int64{TailBytes, contextBytes} {
		entries, err := Tail(path, n)
		if err != nil {
			return "", err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; own(e, false) && e.Slug != "" {
				return e.Slug, nil
			}
		}
	}
	return "", nil
}
