package decide

import "strings"

// ReviewReady tells the human that every phase of the plan but its QA phases is done (state.ReviewReady),
// and that what they find in a review of theirs now reaches qa, the QA phases whose sessions take their
// words, in time. It asks nothing of them.
func ReviewReady(qa []string) string {
	handles := make([]string, len(qa))
	for i, id := range qa {
		handles[i] = "`@" + id + "`"
	}
	phase := "the QA phase"
	if len(qa) > 1 {
		phase = "the QA phases"
	}
	return "Every phase but " + phase + " has landed. If you review now, what you find reaches " + phase + " in time: write it to " +
		orList(handles) + ", or add phases. Nothing waits on you."
}

// ReviewReadyLine is ReviewReady as baton says it in the lead's session.
func ReviewReadyLine(qa []string) string { return "baton: " + ReviewReady(qa) }

// orList joins items as "a", "a or b", "a, b or c".
func orList(items []string) string {
	if n := len(items); n > 1 {
		return strings.Join(items[:n-1], ", ") + " or " + items[n-1]
	}
	return strings.Join(items, "")
}
