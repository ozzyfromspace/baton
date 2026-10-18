package gitx

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// The risk assessment (loop/assess.go) reads what changed since the last full check passed: the diffstat,
// and the patch with its largest hunks clipped to fit what a model is given.

// Diff is what changed in the repository at root from commit from to commit to: git's diffstat, and the
// patch with two lines of context. A rename counts as a deletion and an addition, as Stats counts it.
func Diff(ctx context.Context, root, from, to string) (stat, patch string, err error) {
	if !Usable(root) {
		return "", "", ErrNoGit
	}
	base := []string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "--no-renames"}
	s, err := run(ctx, root, nil, nil, append(base, "--stat=120,80", from, to, "--")...)
	if err != nil {
		return "", "", err
	}
	p, err := run(ctx, root, nil, nil, append(base, "-U2", from, to, "--")...)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(string(s), "\n"), string(p), nil
}

// hunkCaps are the lines a hunk keeps, tried from the most (every line) until the patch fits.
var hunkCaps = []int{math.MaxInt, 60, 30, 15, 8, 4}

// ClipPatch fits patch, as Diff gives it, into budget characters: past its budget, each hunk past a cap of
// lines keeps its first lines and says how many more it had, the cap lowered until the patch fits; the
// files whose patch still does not fit are left out at the end, and counted. The files leave names (a
// lockfile, a generated file) keep their headers, and their hunks are left out.
func ClipPatch(patch string, budget int, leave func(path string) bool) string {
	files := splitPatch(patch)
	if len(files) == 0 || budget <= 0 {
		return ""
	}
	var out string
	for _, n := range hunkCaps {
		out = joinPatch(files, n, leave, budget)
		if len(out) <= budget {
			return out
		}
	}
	return out
}

// patchFile is one file of a patch: its header lines (from "diff --git" to the first hunk), its path, and
// its hunks, each its lines from its "@@" line on.
type patchFile struct {
	header []string
	path   string
	hunks  [][]string
}

// splitPatch reads a patch into its files.
func splitPatch(patch string) []patchFile {
	var files []patchFile
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, patchFile{header: []string{line}, path: patchPath(line)})
		case len(files) == 0:
			continue
		case strings.HasPrefix(line, "@@"):
			f := &files[len(files)-1]
			f.hunks = append(f.hunks, []string{line})
		default:
			f := &files[len(files)-1]
			if len(f.hunks) == 0 {
				f.header = append(f.header, line)
				if p, ok := strings.CutPrefix(line, "+++ b/"); ok {
					f.path = p
				}
			} else {
				f.hunks[len(f.hunks)-1] = append(f.hunks[len(f.hunks)-1], line)
			}
		}
	}
	return files
}

// patchPath is the path a "diff --git a/<path> b/<path>" line names.
func patchPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git a/")
	if i := strings.Index(rest, " b/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// joinPatch writes files back with each hunk cut to n lines past its "@@" line, the files leave names with
// no hunks, and stops at the first file past budget, counting the files left out.
func joinPatch(files []patchFile, n int, leave func(string) bool, budget int) string {
	var b strings.Builder
	for i, f := range files {
		var part strings.Builder
		for _, h := range f.header {
			part.WriteString(h + "\n")
		}
		if leave != nil && leave(f.path) {
			if len(f.hunks) > 0 {
				part.WriteString("… (its changes left out: a lockfile or a generated file)\n")
			}
		} else {
			for _, h := range f.hunks {
				body := h[1:]
				part.WriteString(h[0] + "\n")
				for _, l := range body[:min(len(body), n)] {
					part.WriteString(l + "\n")
				}
				if len(body) > n {
					fmt.Fprintf(&part, "… (%d more lines of this hunk)\n", len(body)-n)
				}
			}
		}
		if b.Len()+part.Len() > budget && n == hunkCaps[len(hunkCaps)-1] {
			fmt.Fprintf(&b, "… (the changes of %d more files left out; the diffstat lists them)\n", len(files)-i)
			break
		}
		b.WriteString(part.String())
	}
	return b.String()
}
