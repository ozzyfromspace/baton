package decide

import "fmt"

// Compacted is the line under a post-compaction brief that wakes the model after a compaction baton asked
// for (title: "P3 (Title)"): after a boundary or a switch (starts) the phase begins, or resumes when it was
// set aside unfinished (resumed); after a checkpoint it goes on.
func Compacted(title string, starts, resumed bool) string {
	if !starts {
		return fmt.Sprintf("[baton] Context compacted at your checkpoint. Continue %s.", title)
	}
	verb := "Begin"
	if resumed {
		verb = "Resume"
	}
	return fmt.Sprintf("[baton] Context compacted. %s %s now — the brief above has your instructions.", verb, title)
}
