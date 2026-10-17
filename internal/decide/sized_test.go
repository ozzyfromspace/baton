package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The words of a landing's verification again sized to the change: why it was skipped, what the landing's
// announcement and the lead are told of it, and the land check.
func TestSizedReverificationWords(t *testing.T) {
	for _, c := range []struct {
		name, got, want string
	}{
		{"no file shared", NoSharedFiles("main", 3, 1, nil), "the 3 files changed on main since its gate share none with the 1 it changes"},
		{"only Markdown", NoSharedFiles("main", 1, 2, []string{"README.md", "docs/x.md"}),
			"the 1 file changed on main since its gate share only Markdown (README.md, docs/x.md) with the 2 it changes"},
		{"announced", SkippedOver([]string{"aaaaaaaaaa", "bbbbbbbbbb"}, "P15"),
			", over 2 commits its gate did not run with (aaaaaaa, bbbbbbb), which change none of its files, Markdown aside: not verified again, as P15, the QA phase ahead, checks them together"},
		{"recorded", LandCheckRecorded("P7", "go test ./x"),
			"baton: land check recorded — if other work lands before P7 does, baton verifies P7 again with `go test ./x` instead of its last passing gate."},
		{"the lead's", LandCheckLead("P0"), "baton: note — a land check is for a phase agent's landing; P0 is the lead's own, so --land-check recorded nothing."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

// The lead is told of a landing over its own commits that its next gate runs them together, and of one
// whose verification again was skipped that the QA phase ahead checks them together: never that those
// commits are its own.
func TestLandedTellsWhoChecksWhatItLandedOver(t *testing.T) {
	ours := state.Landing{Phase: "P1", Commits: []string{"1111111111"}, Files: []string{"one.txt"}, Head: "1111111111", Verified: "0000000000", Unverified: []string{"2222222222"}}
	skipped := state.Landing{Phase: "P2", Commits: []string{"3333333333"}, Files: []string{"two.txt"}, Head: "3333333333", Verified: "4444444444",
		Unverified: []string{"5555555555", "6666666666"}, QA: "P5"}
	for _, c := range []struct {
		name    string
		ls      []state.Landing
		want    []string
		without []string
	}{
		{"over the lead's commit", []state.Landing{ours},
			[]string{"P1's gate passed on 0000000, before your commit 2222222; your next gate runs them together. Never amend"}, []string{"QA phase"}},
		{"skipped for the QA phase", []state.Landing{skipped},
			[]string{"P2's gate passed on 4444444, before commits 5555555, 6666666, which change none of its files, Markdown aside; P5, the QA phase ahead, checks them together. Never amend"},
			[]string{"your commit", "your next gate"}},
		{"both", []state.Landing{ours, skipped},
			[]string{"before your commit 2222222; your next gate runs them together. P2's gate passed on 4444444", "P5, the QA phase ahead, checks them together. Never amend"}, nil},
		{"verified", []state.Landing{{Phase: "P3", Commits: []string{"7777777777"}, Files: []string{"x"}, Head: "7777777777"}}, nil, []string{"gate"}},
	} {
		got := Landed(c.ls, "main")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
		for _, w := range c.without {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q says %q", c.name, got, w)
			}
		}
	}
}

// A phase agent is told of the land check where it learns how to report its phase done; the lead is not.
func TestAPhaseAgentIsToldOfTheLandCheck(t *testing.T) {
	agent := Words{Git: true, Worker: true}.Reporting()
	if !strings.Contains(agent, LandCheckHow) || !strings.Contains(agent, "`baton done <phase> --land-check \"<command>\"`") {
		t.Errorf("a phase agent is not told of the land check:\n%s", agent)
	}
	if lead := (Words{Git: true}).Reporting(); strings.Contains(lead, "--land-check") {
		t.Errorf("the lead is told of the land check:\n%s", lead)
	}
}
