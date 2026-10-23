package decide

import (
	"strings"
	"testing"
	"time"
)

// The lead is told never to make the human a courier, in its primer (Reporting) and at the end of each
// brief after a compaction (Closing); a phase agent's words, which a worker's brief ends with, say nothing
// of it.
func TestTheLeadIsToldNobodyIsACourier(t *testing.T) {
	lead := Words{Git: true, Timeout: 10 * time.Minute}
	agent := Words{Git: true, Timeout: 10 * time.Minute, Worker: true}
	for _, c := range []struct {
		name string
		text string
		want bool
	}{
		{"the lead's primer", lead.Reporting(), true},
		{"the lead's brief", lead.Closing("P4"), true},
		{"no repository", Words{Timeout: time.Minute}.Closing("P4"), true},
		{"a worker's brief", agent.Reporting(), false},
		{"a worker's closing", agent.Closing("P4"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Contains(c.text, "Never ask the human to carry words between sessions. A phase agent gets baton's messages and does its own merges; "+
				"`@<phase>` is the human's own way to write to one."); got != c.want {
				t.Errorf("says it: %v, want %v:\n%s", got, c.want, c.text)
			}
		})
	}
}
