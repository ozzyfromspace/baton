package host

import (
	"strings"
	"testing"
)

func TestRingKeepsTheTail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int
		writes []string
		want   string
	}{
		{"empty", 8, nil, ""},
		{"under the size", 8, []string{"abc", "de"}, "abcde"},
		{"exactly the size", 8, []string{"abcd", "efgh"}, "abcdefgh"},
		{"wraps", 8, []string{"abcdef", "ghij"}, "cdefghij"},
		{"wraps again", 8, []string{"abcdef", "ghij", "klmnop"}, "ijklmnop"},
		{"one write over the size", 8, []string{"ab", "0123456789"}, "23456789"},
		{"after a write over the size", 8, []string{"0123456789", "xy"}, "456789xy"},
		{"byte by byte", 4, strings.Split("abcdefg", ""), "defg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRing(tc.size)
			for _, w := range tc.writes {
				if n, err := r.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write(%q) = %d, %v", w, n, err)
				}
			}
			if got := string(r.Bytes()); got != tc.want {
				t.Fatalf("Bytes() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModeTrackerFollowsPrivateModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		feeds []string
		want  string
	}{
		{"none", []string{"hello\r\n\x1b[2K\x1b[1;1H"}, ""},
		{"set at startup", []string{"\x1b[?1004h\x1b[?2031h"}, "\x1b[?1004h\x1b[?2031h"},
		{"several in one", []string{"\x1b[?2004;25h"}, "\x1b[?2004h\x1b[?25h"},
		{"the latest state, in the order first seen", []string{"\x1b[?25l\x1b[?1004h\x1b[?25h"}, "\x1b[?25h\x1b[?1004h"},
		{"reset", []string{"\x1b[?1004h", "\x1b[?1004l"}, "\x1b[?1004l"},
		{"split across reads", []string{"ab\x1b", "[?10", "04", "h"}, "\x1b[?1004h"},
		{"synchronized output is never replayed", []string{"\x1b[?2026h\x1b[?2004h\x1b[?2026l"}, "\x1b[?2004h"},
		{"not private", []string{"\x1b[4h\x1b[>4;2m\x1b[>5u"}, ""},
		{"not a number", []string{"\x1b[?x1h"}, ""},
		{"an escape restarts a sequence", []string{"\x1b[?10\x1b[?7h"}, "\x1b[?7h"},
		{"overlong", []string{"\x1b[?" + strings.Repeat("1;", csiMax) + "h\x1b[?7l"}, "\x1b[?7l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m modeTracker
			for _, f := range tc.feeds {
				m.feed([]byte(f))
			}
			if got := string(m.sequences()); got != tc.want {
				t.Fatalf("sequences() = %q, want %q", got, tc.want)
			}
		})
	}
}
