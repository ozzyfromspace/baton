package host

import (
	"strconv"
	"strings"
)

// The host must know whether a *human* is typing, and whether the input box may hold their draft:
// baton never types over a draft, and holds off while someone is at the keyboard. But not every byte
// on stdin is a keystroke. The terminal also answers queries Claude Code sends (cursor position, device
// attributes, keyboard-protocol flags, colours) and reports focus changes and mouse events.
//
// keyTracker is a byte-level state machine (escape sequences split across reads are handled) that
// keeps a rough count of what the input box holds: printable characters and pastes add to it,
// backspace takes away, Enter, Ctrl-C (which empties Claude Code's input box) and Ctrl-U on a single
// line clear it. The count errs on the side of "there is a draft": anything it cannot account for
// (history recall, a multi-line draft) counts as text, so baton holds off and, if it must, asks the
// human to press Enter or Ctrl-C.
type keyTracker struct {
	esc     []byte // an escape sequence in progress
	inPaste bool   // inside a bracketed paste (ESC[200~ … ESC[201~)
	skip    int    // raw bytes still to ignore (the payload of an X10 mouse report)
	chars   int    // characters on the current line of the draft
	lines   int    // completed lines of the draft (Shift+Enter, "\" + Enter, pasted newlines)
	escaped bool   // the last character was "\": Claude Code reads the next Enter as a newline
}

// draft reports whether the input box may hold text the human typed.
func (k *keyTracker) draft() bool { return k.chars > 0 || k.lines > 0 }

// feed processes a chunk of stdin and reports whether any of it came from the human (as opposed to
// terminal reports).
func (k *keyTracker) feed(b []byte) (human bool) {
	for _, c := range b {
		if k.skip > 0 {
			k.skip--
			continue
		}
		if len(k.esc) > 0 {
			k.esc = append(k.esc, c)
			if done, h := k.finishEscape(); done {
				human = human || h
			}
			continue
		}
		if c == 0x1b {
			k.esc = append(k.esc[:0], c)
			continue
		}
		human = true
		if k.inPaste {
			if c == '\r' || c == '\n' {
				k.newline()
			} else if c >= 0x20 {
				k.chars++
			}
			continue
		}
		k.key(c)
	}
	// A lone ESC at the end of a read is the Escape key, not the start of a sequence: terminals send a
	// sequence in one write. It does not change the box (Escape does not clear Claude Code's input).
	if len(k.esc) == 1 {
		k.esc = k.esc[:0]
		human = true
	}
	return human
}

// key applies one plain byte typed outside a paste.
func (k *keyTracker) key(c byte) {
	escaped := k.escaped
	k.escaped = false
	switch {
	case c == '\r' || c == '\n':
		if escaped { // "\" + Enter inserts a newline
			k.newline()
			return
		}
		k.clear()
	case c == 0x03: // Ctrl-C empties the input box
		k.clear()
	case c == 0x15: // Ctrl-U deletes the current line
		k.chars = 0
	case c == 0x7f || c == 0x08: // backspace
		k.backspace()
	case c < 0x20: // other control keys move the cursor or open views: the box is unchanged
	default:
		k.chars++
		k.escaped = c == '\\'
	}
}

func (k *keyTracker) clear()   { k.chars, k.lines = 0, 0 }
func (k *keyTracker) newline() { k.lines++; k.chars = 0 }
func (k *keyTracker) backspace() {
	switch {
	case k.chars > 0:
		k.chars--
	case k.lines > 0:
		k.lines-- // joined with the line above, whose length is unknown: assume it holds text
		k.chars = 1
	}
}

// finishEscape decides whether k.esc is a complete sequence and, if so, applies it and reports whether
// it came from the human.
func (k *keyTracker) finishEscape() (done, human bool) {
	s := k.esc
	if len(s) < 2 {
		return false, false
	}
	reset := func(h bool) (bool, bool) { k.esc = k.esc[:0]; return true, h }
	switch s[1] {
	case '[': // CSI: parameters 0x30–0x3f, intermediates 0x20–0x2f, final 0x40–0x7e
		last := s[len(s)-1]
		if len(s) == 2 || last < 0x40 || last > 0x7e {
			if len(s) > 64 {
				return reset(true)
			}
			return false, false
		}
		body := string(s[2 : len(s)-1])
		switch {
		case body == "200" && last == '~':
			k.inPaste = true
			return reset(true)
		case body == "201" && last == '~':
			k.inPaste = false
			return reset(true)
		case len(s) == 3 && (last == 'I' || last == 'O'): // focus in / out
			return reset(false)
		case body == "" && last == 'M': // X10 mouse report: three raw bytes follow
			k.skip = 3
			return reset(true)
		case strings.HasPrefix(body, "<") && (last == 'M' || last == 'm'): // SGR mouse report
			return reset(true)
		case last == 'R' && body != "": // cursor position report
			return reset(false)
		case last == 'c' && len(body) > 0 && (body[0] == '?' || body[0] == '>'): // device attributes
			return reset(false)
		case last == 'u' && len(body) > 0 && body[0] == '?': // keyboard protocol flags report
			return reset(false)
		case last == 'y' && len(body) > 0 && body[0] == '?': // DECRPM mode report
			return reset(false)
		case last == 'u': // a key in the kitty keyboard protocol
			k.kittyKey(body)
			return reset(true)
		case (last == 'A' || last == 'B') && !k.draft(): // Up/Down on an empty box recall history
			k.chars = 1
			return reset(true)
		}
		return reset(true) // arrows, function keys, Shift-Tab: the human, but the box is unchanged
	case ']', 'P', '_', '^': // OSC / DCS / APC / PM: terminal replies, end with BEL or ST (ESC \)
		last := s[len(s)-1]
		if last == 0x07 || (len(s) >= 2 && s[len(s)-2] == 0x1b && last == '\\' && len(s) > 3) {
			return reset(false)
		}
		if len(s) > 4096 {
			return reset(false)
		}
		return false, false
	case 'O': // SS3: F1–F4 / application cursor keys
		if len(s) < 3 {
			return false, false
		}
		if (s[2] == 'A' || s[2] == 'B') && !k.draft() {
			k.chars = 1
		}
		return reset(true)
	case '\r': // Alt+Enter inserts a newline
		k.newline()
		return reset(true)
	case 0x7f: // Alt+Backspace deletes a word: assume text remains
		return reset(true)
	default: // Alt+key and other two-byte escapes
		return reset(true)
	}
}

// kittyKey applies a key reported in the kitty keyboard protocol: CSI codepoint[;modifiers] u.
func (k *keyTracker) kittyKey(body string) {
	parts := strings.Split(body, ";")
	cp, err := strconv.Atoi(strings.Split(parts[0], ":")[0])
	if err != nil {
		return
	}
	mods := 1
	if len(parts) > 1 {
		if m, err := strconv.Atoi(strings.Split(parts[1], ":")[0]); err == nil {
			mods = m
		}
	}
	ctrl := (mods-1)&4 != 0
	plain := mods == 1 || mods == 2 // none, or Shift
	switch {
	case cp == 13 && mods == 1:
		k.key('\r')
	case cp == 13: // Shift/Alt/Ctrl+Enter insert a newline
		k.newline()
	case cp == 127 || cp == 8:
		k.backspace()
	case ctrl && cp == 'c':
		k.clear()
	case ctrl && cp == 'u':
		k.chars = 0
	case plain && cp >= 32 && cp != 127:
		k.chars++
		k.escaped = cp == '\\'
	}
}
