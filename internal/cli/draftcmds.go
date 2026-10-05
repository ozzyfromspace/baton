package cli

import (
	"fmt"
	"strconv"
	"strings"
)

func init() {
	register("drafts", "list drafts baton saved out of the input box: drafts [--last | N] [--path]", cmdDrafts)
}

// cmdDrafts hands back what baton took out of the input box.
//
// baton clears a draft rather than waiting on it, because a half-typed message used to be able to hold
// a whole run (measured 2026-10-05: a compaction queued for 35 minutes). Nothing is thrown away — each
// draft is a file holding exactly the text and nothing else, so `cat` recovers it even if baton will
// not start. This command is the convenience, not the mechanism.
func cmdDrafts(args []string, io IO) int {
	s, err := store(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	drafts, err := s.Drafts()
	if err != nil {
		return fail(io, "reading drafts: %v", err)
	}

	want, pathOnly := -1, false
	for _, a := range args {
		switch {
		case a == "--last":
			want = 0
		case a == "--path":
			pathOnly = true
		case strings.HasPrefix(a, "-"):
			return fail(io, "unknown option %q — usage: baton drafts [--last | N] [--path]", a)
		default:
			n, convErr := strconv.Atoi(a)
			if convErr != nil || n < 1 {
				return fail(io, "expected a draft number (1 is the newest), got %q", a)
			}
			want = n - 1
		}
	}

	if len(drafts) == 0 {
		fmt.Fprintln(io.Out, "baton: no saved drafts. baton only saves one when it has to clear the input box to get on with the plan.")
		return 0
	}

	// One draft, printed raw so it can be piped or pasted straight back.
	if want >= 0 {
		if want >= len(drafts) {
			return fail(io, "there %s only %d saved draft%s", plural(len(drafts), "is", "are"), len(drafts), plural(len(drafts), "", "s"))
		}
		d := drafts[want]
		if pathOnly {
			fmt.Fprintln(io.Out, d.Path)
			return 0
		}
		fmt.Fprint(io.Out, d.Text)
		if !strings.HasSuffix(d.Text, "\n") {
			fmt.Fprintln(io.Out)
		}
		return 0
	}

	fmt.Fprintf(io.Out, "baton: %d saved draft%s (newest first) — `baton drafts --last` prints the newest, `baton drafts N` any of them\n",
		len(drafts), plural(len(drafts), "", "s"))
	for i, d := range drafts {
		when := "unknown time"
		if !d.At.IsZero() {
			when = d.At.Local().Format("Mon 15:04")
		}
		phase := ""
		if d.Phase != "" {
			phase = " · " + d.Phase
		}
		fmt.Fprintf(io.Out, "  %d. %s%s · %d chars — %s\n", i+1, when, phase, len(d.Text), d.Preview(60))
		if pathOnly {
			fmt.Fprintf(io.Out, "     %s\n", d.Path)
		}
	}
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
