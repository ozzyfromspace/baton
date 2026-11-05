package lane

import (
	"path/filepath"
	"slices"
	"strings"
)

// IsWaitLoop reports whether command, a shell command line, waits by polling: a `while` or `until` loop with
// a sleep in it (its condition or its body), or a `tail` that follows a file (-f, -F, --follow). Claude Code
// wakes the model when a background task ends, so such a command waits on nothing it needs; started in the
// background, it can run for as long as the session lives. In a real run of a 74-phase campaign one polled
// for a line that never came, every 5 seconds, for 22 hours, writing nothing.
//
// It reads the line as IsCheck does, word by word with no shell (simpleCommands): each simple command is
// looked at past the reserved words that lead it (do, then, else, !, {), and a `sh -c` script is looked
// into. PowerShell's loops read the same way (`while (…) { Start-Sleep 5 }`, `do { … } until (…)`). A
// `for` loop is no wait: it ends when its list does.
func IsWaitLoop(command string) bool { return waitLoop(command, 0) }

// waitLoop is IsWaitLoop, depth bounding the sh -c scripts looked into.
func waitLoop(command string, depth int) bool {
	var loops []bool // the loops open at this point of the line, innermost last: true for while and until
	inWait := func() bool { return slices.Contains(loops, true) }
	for _, words := range simpleCommands(command) {
		w := words
	keywords:
		for len(w) > 0 {
			switch word := strings.TrimPrefix(w[0], "{"); strings.ToLower(word) {
			case "while", "until":
				loops = append(loops, true)
			case "for", "select", "foreach":
				loops = append(loops, false)
				w = nil // the rest is its list, up to its do
				break keywords
			case "do":
				if len(loops) == 0 {
					loops = append(loops, true) // PowerShell's do { … } while (…) or until (…)
				}
			case "done":
				if len(loops) > 0 {
					loops = loops[:len(loops)-1]
				}
			case "", "then", "else", "elif", "if", "!", "}":
			default:
				if word != w[0] {
					w = append([]string{word}, w[1:]...)
				}
				break keywords
			}
			w = w[1:]
		}
		w = unwrap(w)
		if len(w) == 0 {
			continue
		}
		head, args := filepath.Base(w[0]), w[1:]
		switch {
		case isSleep(head) && inWait():
			return true
		case head == "tail" || head == "gtail":
			if slices.ContainsFunc(args, follows) {
				return true
			}
		case depth < 2 && slices.Contains([]string{"sh", "bash", "zsh", "dash"}, head):
			if i := slices.Index(args, "-c"); i >= 0 && i+1 < len(args) && waitLoop(args[i+1], depth+1) {
				return true
			}
		}
	}
	return false
}

// isSleep reports a command that sleeps: sleep (gsleep, coreutils' on macOS), or PowerShell's Start-Sleep.
func isSleep(head string) bool {
	h := strings.ToLower(head)
	return h == "sleep" || h == "gsleep" || h == "start-sleep"
}

// follows reports an argument of tail's that has it follow the file: --follow (with or without a value), or
// a cluster of short options holding f or F (-f, -F, -fn 100, -n0F).
func follows(arg string) bool {
	if arg == "--follow" || strings.HasPrefix(arg, "--follow=") {
		return true
	}
	opts, ok := strings.CutPrefix(arg, "-")
	if !ok || opts == "" || strings.HasPrefix(opts, "-") {
		return false
	}
	for _, r := range opts {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return false
		}
	}
	return strings.ContainsAny(opts, "fF")
}
