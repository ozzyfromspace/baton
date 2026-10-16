package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// A plan approved in plan mode is the session's plan-mode file, <plansDirectory>/<slug>.md, and Claude Code
// attaches the whole of that file to the conversation after every compaction for as long as it exists
// (spike 38): tens of thousands of tokens a phase's brief already quotes in part. baton moves the plan it
// attaches out of that path, in the same folder, to a name of its own (MoveAside), and follows it there.

// asideMax is the most characters of a plan's title AsideName keeps.
const asideMax = 60

// AsideName is the file name baton moves a plan titled title to: the title's letters and digits in lower
// case, every other run of characters a hyphen (an apostrophe none), cut after a whole word to at most
// asideMax characters, then ".md"; "plan.md" for a title with no letter or digit.
func AsideName(title string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r == '\'' || r == '’':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			gap = false
		default:
			gap = true
		}
	}
	name := b.String()
	if len(name) > asideMax {
		cut := name[:asideMax+1]
		if i := strings.LastIndexByte(cut, '-'); i > 0 {
			name = cut[:i]
		} else {
			name = strings.ToValidUTF8(name[:asideMax], "")
		}
	}
	if name == "" {
		name = "plan"
	}
	return name + ".md"
}

// asideTries is how many names MoveAside tries: AsideName, then with -2 up to -asideTries.
const asideTries = 99

// MoveAside renames file, in its own folder, to a name from title no file there has (AsideName, with -2,
// -3… before ".md" while that is taken), never replacing a file, and returns the path it has now.
func MoveAside(file, title string) (string, error) {
	dir, base := filepath.Dir(file), strings.TrimSuffix(AsideName(title), ".md")
	for n := 1; n <= asideTries; n++ {
		name := base + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", base, n)
		}
		to := filepath.Join(dir, name)
		switch err := renameNew(file, to); {
		case err == nil:
			return to, nil
		case !errors.Is(err, fs.ErrExist):
			return "", err
		}
	}
	return "", fmt.Errorf("no free name for %q beside %s", title, file)
}

// renameNew renames from to to, which must not exist: fs.ErrExist if it does. A hard link refuses an
// existing name by itself; where links are not supported, the name is looked at first.
func renameNew(from, to string) error {
	if _, err := os.Lstat(from); err != nil {
		return err
	}
	err := os.Link(from, to)
	switch {
	case err == nil:
		if err := os.Remove(from); err != nil {
			os.Remove(to) // the file stays where it was, under its one name
			return err
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}
