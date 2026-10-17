package gitx

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// The merge queue sizes a landing's re-verification to the change (loop/sized.go): it compares the files
// that landed on the run's branch since the phase's gate with the files the phase changes, from the
// point its branch and the run's last met.

// MergeBase is the best common ancestor of commits a and b, in the repository at root: where their
// histories last met. It errs when they share none.
func MergeBase(ctx context.Context, root, a, b string) (string, error) {
	if !Usable(root) {
		return "", ErrNoGit
	}
	out, stderr, code, err := runCode(ctx, root, nil, "merge-base", a, b)
	switch {
	case err == nil:
		return strings.TrimSpace(string(out)), nil
	case code == 1:
		return "", fmt.Errorf("git merge-base: %s and %s share no history", a, b)
	}
	return "", fmt.Errorf("git merge-base: %v: %s", err, firstLines(stderr, 3))
}

// ChangedBetween lists the paths that differ between commits base and to, relative to the repository's
// root and slash-separated, a rename as both its old and its new path. It is sorted.
func ChangedBetween(ctx context.Context, root, base, to string) ([]string, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	out, err := run(ctx, root, nil, nil, "diff", "--no-renames", "--name-only", "-z", base, to)
	if err != nil {
		return nil, err
	}
	paths := nulList(out)
	slices.Sort(paths)
	return paths, nil
}
