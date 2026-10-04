package host

// The host must know whether a *human* is typing: baton never types over a draft, and holds off while
// someone is at the keyboard. But not every byte on stdin is a keystroke. The terminal also answers
// queries Claude Code sends (cursor position, device attributes, keyboard-protocol flags, colours) and
// reports focus changes. keyTracker separates the two.

// inputKind classifies one chunk of stdin.
type inputKind int

const (
	inputNone   inputKind = iota // only terminal reports: not human activity
	inputTyping                  // the human typed or edited: the input box may hold a draft
	inputSubmit                  // the human pressed Enter (outside a paste): the box is empty again
)

// keyTracker classifies stdin as it streams through. It is a byte-level state machine so escape
// sequences split across reads are handled.
type keyTracker struct {
	esc     []byte // an escape sequence in progress
	inPaste bool   // inside a bracketed paste (ESC[200~ … ESC[201~)
}

// feed classifies a chunk by the last human event in it: "hi\r" leaves the box empty (submit), "\rx"
// leaves a draft (typing), and terminal reports alone are no human activity at all (none).
func (k *keyTracker) feed(b []byte) inputKind {
	kind := inputNone
	mark := func(x inputKind) {
		if x != inputNone {
			kind = x
		}
	}
	for _, c := range b {
		if len(k.esc) > 0 {
			k.esc = append(k.esc, c)
			if done, x := k.finishEscape(); done {
				mark(x)
			}
			continue
		}
		switch {
		case c == 0x1b:
			k.esc = append(k.esc[:0], c)
		case (c == '\r' || c == '\n') && !k.inPaste:
			mark(inputSubmit)
		default:
			mark(inputTyping)
		}
	}
	return kind
}

// finishEscape decides whether k.esc is a complete sequence and, if so, what it means.
func (k *keyTracker) finishEscape() (bool, inputKind) {
	s := k.esc
	if len(s) < 2 {
		return false, inputNone
	}
	reset := func(x inputKind) (bool, inputKind) { k.esc = k.esc[:0]; return true, x }
	switch s[1] {
	case '[': // CSI: parameters 0x30–0x3f, intermediates 0x20–0x2f, final 0x40–0x7e
		last := s[len(s)-1]
		if len(s) == 2 || last < 0x40 || last > 0x7e {
			if len(s) > 64 {
				return reset(inputTyping)
			}
			return false, inputNone
		}
		body := string(s[2 : len(s)-1])
		switch {
		case body == "200" && last == '~':
			k.inPaste = true
			return reset(inputTyping)
		case body == "201" && last == '~':
			k.inPaste = false
			return reset(inputTyping)
		case len(s) == 3 && (last == 'I' || last == 'O'): // focus in / out
			return reset(inputNone)
		case last == 'R' && body != "": // cursor position report
			return reset(inputNone)
		case last == 'c' && len(body) > 0 && (body[0] == '?' || body[0] == '>'): // device attributes
			return reset(inputNone)
		case last == 'u' && len(body) > 0 && body[0] == '?': // keyboard protocol flags report
			return reset(inputNone)
		case last == 'y' && len(body) > 0 && body[0] == '?': // DECRPM mode report
			return reset(inputNone)
		case last == 'u' && (body == "13" || startsWith(body, "13;")) && !k.inPaste: // kitty-protocol Enter
			return reset(inputSubmit)
		}
		return reset(inputTyping) // arrows, function keys, CSI-u keys: the human
	case ']', 'P', '_', '^': // OSC / DCS / APC / PM: terminal replies, end with BEL or ST (ESC \)
		last := s[len(s)-1]
		if last == 0x07 || (len(s) >= 2 && s[len(s)-2] == 0x1b && last == '\\' && len(s) > 3) {
			return reset(inputNone)
		}
		if len(s) > 4096 {
			return reset(inputNone)
		}
		return false, inputNone
	case 'O': // SS3: F1–F4 / application cursor keys
		if len(s) < 3 {
			return false, inputNone
		}
		return reset(inputTyping)
	default: // Alt+key and other two-byte escapes
		return reset(inputTyping)
	}
}

func startsWith(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
