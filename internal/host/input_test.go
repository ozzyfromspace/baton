package host

import "testing"

func TestKeyTrackerClassifiesInput(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   inputKind
	}{
		{"plain typing", []string{"hello"}, inputTyping},
		{"enter", []string{"\r"}, inputSubmit},
		{"type then enter", []string{"hi\r"}, inputSubmit},
		{"enter then type again leaves a draft", []string{"\rx"}, inputTyping},
		{"focus in", []string{"\x1b[I"}, inputNone},
		{"focus out", []string{"\x1b[O"}, inputNone},
		{"cursor position report", []string{"\x1b[24;80R"}, inputNone},
		{"device attributes", []string{"\x1b[?62;22c"}, inputNone},
		{"kitty flags report", []string{"\x1b[?5u"}, inputNone},
		{"DECRPM", []string{"\x1b[?2026;2$y"}, inputNone},
		{"OSC colour reply (BEL)", []string{"\x1b]11;rgb:0000/0000/0000\x07"}, inputNone},
		{"OSC colour reply (ST)", []string{"\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, inputNone},
		{"DCS reply", []string{"\x1bP>|iTerm2 3.6\x1b\\"}, inputNone},
		{"report split across reads", []string{"\x1b[2", "4;8", "0R"}, inputNone},
		{"arrow key", []string{"\x1b[A"}, inputTyping},
		{"ctrl-u", []string{"\x15"}, inputTyping},
		{"alt-b", []string{"\x1bb"}, inputTyping},
		{"kitty enter", []string{"\x1b[13u"}, inputSubmit},
		{"kitty enter with mods", []string{"\x1b[13;1u"}, inputSubmit},
		{"kitty letter", []string{"\x1b[97u"}, inputTyping},
		{"paste with newlines does not submit", []string{"\x1b[200~line one\rline two\x1b[201~"}, inputTyping},
		{"enter after paste submits", []string{"\x1b[200~x\x1b[201~", "\r"}, inputSubmit},
	}
	for _, c := range cases {
		var k keyTracker
		got := inputNone
		for _, ch := range c.chunks {
			got = k.feed([]byte(ch))
		}
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
