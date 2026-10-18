package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A phase's record names it, says when and by whom it was done, and gives its notes and its commits, at
// most maxRecordCommits of them.
func TestRecordEntryText(t *testing.T) {
	at := time.Date(2026, 10, 17, 14, 2, 0, 0, time.UTC)
	var many []string
	for i := range 32 {
		many = append(many, fmt.Sprintf("%07d commit %d", i, i))
	}
	for _, c := range []struct {
		name string
		e    RecordEntry
		want string
	}{
		{"a lead phase", RecordEntry{Phase: "P3", Title: "The record", At: at, Notes: "  It works.\nP4 needs X.\n", Commits: []string{"abc1234 feat: one", "def5678 test: two"}},
			"## P3 — The record\n\nDone 2026-10-17 14:02, by the lead.\n\nIt works.\nP4 needs X.\n\nCommits:\n\n- abc1234 feat: one\n- def5678 test: two\n\n"},
		{"a landed phase, no notes, no commits", RecordEntry{Phase: "P4", Title: "Four", At: at, Landed: true},
			"## P4 — Four\n\nLanded 2026-10-17 14:02, from its phase agent.\n\nNo notes.\n\nCommits: none.\n\n"},
	} {
		if got := c.e.Text(); got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
	got := RecordEntry{Phase: "P5", Title: "Five", At: at, Commits: many}.Text()
	if strings.Count(got, "\n- ") != maxRecordCommits+1 || !strings.HasSuffix(got, "- and 2 more\n\n") || strings.Contains(got, "commit 30") {
		t.Errorf("many commits:\n%s", got)
	}
}

// A record's commit message names its phase, its title clipped.
func TestRecordCommit(t *testing.T) {
	if got := RecordCommit("P3", "The record"); got != "chore(baton): record P3 — The record" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", 80)
	if got := RecordCommit("P3", long); !strings.HasSuffix(got, "…") || len([]rune(got)) != len([]rune("chore(baton): record P3 — "))+60 {
		t.Errorf("got %q (%d runes)", got, len([]rune(got)))
	}
}
