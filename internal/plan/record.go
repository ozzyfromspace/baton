package plan

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// A plan may name a record file in its standing rules, on a line of its own:
//
//	Record: docs/record.md
//
// (`**Record:** `docs/record.md`` and a bullet's `- Record: …` too). baton appends each phase's record to
// it as the phase is done (a lead phase) or lands (an agent's), and commits that file alone in the lead's
// checkout (package loop, record.go); no model of the run edits it (package hooks). One file every phase's
// record goes to, written by baton alone, so phases never collide on it, as they do on a progress file each
// of them edits. The path is relative to the project's root, and names a file inside it.

// recordLabel is the label of a plan's record line.
const recordLabel = "record"

// recordOf reads the record file the standing rules name (Plan.Record), checked against the project at
// root ("" checks the path's form alone): "" for rules that name none. A Record: line whose value is prose
// (words with spaces, no code span) names no file, so rules that only speak of records are left alone.
func recordOf(rules, root string) (string, error) {
	var found []string
	for _, line := range strings.Split(rules, "\n") {
		if v, ok := recordLine(line); ok {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return "", nil
	}
	for _, v := range found[1:] {
		if path.Clean(filepath.ToSlash(v)) != path.Clean(filepath.ToSlash(found[0])) {
			return "", fmt.Errorf("the standing rules name two record files (Record: %s and Record: %s); a plan has one: keep one Record: line", found[0], v)
		}
	}
	return CheckRecord(found[0], root)
}

// recordLine reads one line of the standing rules as a Record: line: ok says it is one that names a file,
// and value is that file as written.
func recordLine(line string) (value string, ok bool) {
	l := strings.TrimSpace(line)
	for _, bullet := range []string{"- ", "* ", "+ "} {
		l = strings.TrimSpace(strings.TrimPrefix(l, bullet))
	}
	label, rest, found := strings.Cut(l, ":")
	if !found || !strings.EqualFold(strings.Trim(label, "*_` \t"), recordLabel) {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimLeft(rest, "*_ \t"))
	if spans := codeSpans(rest); len(spans) > 0 {
		return strings.TrimSpace(spans[0]), true
	}
	rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), ".*_"))
	if rest == "" || strings.ContainsFunc(rest, unicode.IsSpace) {
		return "", false // prose about records, not a file
	}
	return rest, true
}

// CheckRecord checks rec, a plan's record file as its Record: line writes it, and returns it as the plan
// keeps it: relative to the project's root, with forward slashes, cleaned. It refuses, saying why, a path
// outside the project (absolute, or climbing out of it), one that names a directory (a trailing slash, the
// root itself, or, with root, a directory there), and one inside git's or baton's own files. root is the
// project's root, or "" to check the path's form alone.
func CheckRecord(rec, root string) (string, error) {
	how := "write the record file's path relative to the project's root, as `Record: docs/record.md`"
	raw := strings.TrimSpace(rec)
	if raw == "" {
		return "", errors.New("Record: names no file; " + how)
	}
	slashed := filepath.ToSlash(raw)
	switch {
	case strings.HasPrefix(raw, "~") || filepath.IsAbs(raw) || strings.HasPrefix(slashed, "/") || filepath.VolumeName(raw) != "":
		return "", fmt.Errorf("Record: %s is outside the project, or may be; %s", raw, how)
	case strings.HasSuffix(slashed, "/"):
		return "", fmt.Errorf("Record: %s names a directory; the record is one file baton appends to: %s", raw, how)
	}
	clean := path.Clean(slashed)
	switch first, _, _ := strings.Cut(clean, "/"); {
	case clean == ".":
		return "", fmt.Errorf("Record: %s names the project's root, a directory; %s", raw, how)
	case first == "..":
		return "", fmt.Errorf("Record: %s climbs out of the project; %s", raw, how)
	case first == ".git" || first == ".baton":
		return "", fmt.Errorf("Record: %s is inside %s, which is not the project's own files; %s", raw, first, how)
	}
	if root != "" {
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean))); err == nil && fi.IsDir() {
			return "", fmt.Errorf("Record: %s names a directory in the project; the record is one file baton appends to: %s", raw, how)
		}
	}
	return clean, nil
}
