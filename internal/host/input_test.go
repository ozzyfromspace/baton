package host

import (
	"testing"
	"time"
)

func TestKeyTrackerFollowsTheInputBox(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		human  bool // the last chunk came from the human
		draft  bool // the box may hold the human's text afterwards
	}{
		{"plain typing", []string{"hello"}, true, true},
		{"enter", []string{"\r"}, true, false},
		{"type then enter", []string{"hi\r"}, true, false},
		{"enter then type again leaves a draft", []string{"\rx"}, true, true},
		{"typed then deleted", []string{"ab", "\x7f", "\x7f"}, true, false},
		{"ctrl-c empties the box", []string{"some words", "\x03"}, true, false},
		{"ctrl-u on one line empties it", []string{"some words", "\x15"}, true, false},
		{"ctrl-u leaves earlier lines", []string{"one\\", "\r", "two", "\x15"}, true, true},
		{"backslash-enter is a newline, not a submit", []string{"one\\", "\r"}, true, true},
		{"shift-enter (kitty) is a newline", []string{"x", "\x1b[13;2u"}, true, true},
		{"alt-enter is a newline", []string{"x", "\x1b\r"}, true, true},
		{"focus in", []string{"\x1b[I"}, false, false},
		{"focus out", []string{"\x1b[O"}, false, false},
		{"cursor position report", []string{"\x1b[24;80R"}, false, false},
		{"device attributes", []string{"\x1b[?62;22c"}, false, false},
		{"kitty flags report", []string{"\x1b[?5u"}, false, false},
		{"DECRPM", []string{"\x1b[?2026;2$y"}, false, false},
		{"OSC colour reply (BEL)", []string{"\x1b]11;rgb:0000/0000/0000\x07"}, false, false},
		{"OSC colour reply (ST)", []string{"\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, false, false},
		{"DCS reply", []string{"\x1bP>|iTerm2 3.6\x1b\\"}, false, false},
		{"report split across reads", []string{"\x1b[2", "4;8", "0R"}, false, false},
		{"mouse wheel (SGR) is not typing", []string{"\x1b[<65;30;12M"}, true, false},
		{"mouse click (X10) is not typing", []string{"\x1b[M #!"}, true, false},
		{"left arrow moves, does not type", []string{"\x1b[D"}, true, false},
		{"shift-tab toggles a mode", []string{"\x1b[Z"}, true, false},
		{"up on an empty box may recall history", []string{"\x1b[A"}, true, true},
		{"escape alone", []string{"\x1b"}, true, false},
		{"escape does not clear a draft", []string{"draft", "\x1b"}, true, true},
		{"alt-b", []string{"x", "\x1bb"}, true, true},
		{"kitty enter", []string{"x", "\x1b[13u"}, true, false},
		{"kitty enter with mods 1", []string{"x", "\x1b[13;1u"}, true, false},
		{"kitty letter", []string{"\x1b[97u"}, true, true},
		{"kitty ctrl-c", []string{"x", "\x1b[99;5u"}, true, false},
		{"kitty backspace", []string{"x", "\x1b[127u"}, true, false},
		{"paste with newlines does not submit", []string{"\x1b[200~line one\rline two\x1b[201~"}, true, true},
		{"enter after paste submits", []string{"\x1b[200~x\x1b[201~", "\r"}, true, false},
		{"delete may remove nothing (cursor at the end): assume text remains", []string{"x", "\x1b[3~"}, true, true},
	}
	for _, c := range cases {
		var k keyTracker
		var human bool
		for _, ch := range c.chunks {
			human = k.feed([]byte(ch), time.Time{})
		}
		if human != c.human || k.draft() != c.draft {
			t.Errorf("%s: human %v draft %v, want %v %v", c.name, human, k.draft(), c.human, c.draft)
		}
	}
}

// 🚨 THE ONE THAT BIT. A bracketed paste latches on ESC[200~ and only a cleanly parsed ESC[201~
// unlatches it — and in paste mode Enter adds a line instead of clearing the box. So a lost end marker
// meant the draft flag could never go back down, and on 2026-10-05 that held a real run's compaction
// for 35 minutes with nothing but a human able to clear it.
func TestALostPasteEndMarkerDoesNotLatchTheDraftForever(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	var k keyTracker
	k.feed([]byte("\x1b[200~pasted text"), t0) // …and the end marker never arrives

	// The control: inside PasteMax this really is a paste, so Enter is a newline and the box keeps text.
	k.feed([]byte("\r"), t0.Add(time.Second))
	if !k.draft() {
		t.Fatal("cleared the box during a live paste")
	}
	// Past the bound it is a lost marker, so Enter clears as it always should.
	k.feed([]byte("\r"), t0.Add(PasteMax+time.Second))
	if k.draft() {
		t.Fatalf("a lost paste end marker still latches the draft: chars=%d lines=%d", k.chars, k.lines)
	}
}

// An X10 mouse report's three payload bytes are skipped blind. ESC is never one of them, so skipping it
// would eat the next sequence's prefix — one way the end marker above goes missing.
func TestABlindMouseSkipDoesNotSwallowTheNextSequence(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	var k keyTracker
	k.feed([]byte("\x1b[200~x"), t0)
	// A mouse report whose payload is truncated, immediately followed by the paste end marker.
	k.feed([]byte("\x1b[M\x1b[201~"), t0)
	k.feed([]byte("\r"), t0)
	if k.draft() {
		t.Fatalf("the skip swallowed the end marker: chars=%d lines=%d", k.chars, k.lines)
	}
}

// baton saves the draft before it clears the box, so the tracker has to remember what was typed.
func TestTheTrackerKeepsTheDraftText(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	var k keyTracker
	k.feed([]byte("hello worldX\x7f"), t0) // a typo, backspaced
	if got := k.draftText(); got != "hello world" {
		t.Fatalf("draft text %q", got)
	}
	k.feed([]byte("\r"), t0) // sent: nothing is left to hand back
	if got := k.draftText(); got != "" {
		t.Fatalf("draft text survived Enter: %q", got)
	}
}
