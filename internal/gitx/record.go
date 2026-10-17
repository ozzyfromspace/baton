package gitx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A plan's record file (plan.Plan.Record) is the one file baton commits into a project: each phase's
// record is appended to it in the lead's checkout, and committed alone (CommitOnly), whatever else the
// checkout has staged or changed. Nothing else is touched: the commit is a new commit of one path, made
// with `git commit --only`, which commits that path's content from the working tree and leaves every other
// entry of the index, and every other file, as it was.

// Commit is one commit, as a phase's record names it.
type Commit struct {
	Hash    string
	Subject string
	When    time.Time // its committer's date
}

// Short is the commit's abbreviated hash.
func (c Commit) Short() string {
	if len(c.Hash) > 7 {
		return c.Hash[:7]
	}
	return c.Hash
}

// logFormat is how Log and Described read commits: hash, committer date and subject, apart.
const logFormat = "--format=%H%x1f%ct%x1f%s"

// Log lists the commits reachable from to and not from from (all of to's when from is ""), oldest first.
func Log(ctx context.Context, root, from, to string) ([]Commit, error) {
	if !Usable(root) {
		return nil, ErrNoGit
	}
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := run(ctx, root, nil, nil, "log", "--reverse", logFormat, rng, "--")
	if err != nil {
		return nil, err
	}
	return parseLog(string(out)), nil
}

// Described reads the commits hashes name, in their order.
func Described(ctx context.Context, root string, hashes []string) ([]Commit, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	if !Usable(root) {
		return nil, ErrNoGit
	}
	out, err := run(ctx, root, nil, nil, append([]string{"log", "--no-walk=unsorted", logFormat}, append(hashes, "--")...)...)
	if err != nil {
		return nil, err
	}
	return parseLog(string(out)), nil
}

func parseLog(out string) []Commit {
	var list []Commit
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		c := Commit{Hash: parts[0], Subject: parts[2]}
		if secs, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			c.When = time.Unix(secs, 0)
		}
		list = append(list, c)
	}
	return list
}

// PathDirty reports whether path (relative to root) has uncommitted work: changed, staged, or untracked.
// Like Dirty, it never takes git's optional locks.
func PathDirty(root, path string) (bool, error) {
	if !Usable(root) {
		return false, ErrNoGit
	}
	ctx, cancel := context.WithTimeout(context.Background(), dirtyTimeout)
	defer cancel()
	out, err := run(ctx, root, nil, nil, "--no-optional-locks", "status", "--porcelain=v1", "-z", "-uall", "--", path)
	if err != nil {
		return false, err
	}
	return len(parseStatus(out)) > 0, nil
}

// LastCommitOf is the latest commit that changed path, or "" for none.
func LastCommitOf(ctx context.Context, root, path string) string {
	if !Usable(root) {
		return ""
	}
	out, err := run(ctx, root, nil, nil, "log", "-1", "--format=%H", "--", path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CommitOnly commits path (relative to root), as it is in the working tree, alone, with message msg, and
// returns the new commit: `git commit --only`, so the rest of the index (what the checkout's own work
// staged) and every other file stay as they were. A path git does not track yet is added first; if the
// commit then fails, it is taken out of the index again, so nothing of baton's stays staged. The project's
// commit hooks are not run (--no-verify): the commit holds only baton's own record, and a hook that checks
// the project's work, or a slow one, would hold the record up for nothing. ErrLocked says another git
// command held the index, and nothing was committed.
func CommitOnly(ctx context.Context, root, path, msg string) (string, error) {
	if !Usable(root) {
		return "", ErrNoGit
	}
	added := false
	if !Tracked(ctx, root, path) {
		if _, stderr, _, err := runCode(ctx, root, landEnv, "add", "--", path); err != nil {
			return "", commitError("add", stderr, err)
		}
		added = true
	}
	if _, stderr, _, err := runCode(ctx, root, landEnv, "commit", "--only", "--no-verify", "-q", "-m", msg, "--", path); err != nil {
		if added {
			runCode(context.Background(), root, landEnv, "rm", "--cached", "-q", "--", path)
		}
		return "", commitError("commit", stderr, err)
	}
	out, err := run(ctx, root, nil, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Tracked reports whether the index has path (relative to root): committed, or staged to be.
func Tracked(ctx context.Context, root, path string) bool {
	if !Usable(root) {
		return false
	}
	_, _, code, err := runCode(ctx, root, landEnv, "ls-files", "--error-unmatch", "--", path)
	return err == nil && code == 0
}

func commitError(what, stderr string, err error) error {
	msg := firstLines(stderr, 3)
	if strings.Contains(stderr, "index.lock") {
		return fmt.Errorf("%w: %s", ErrLocked, msg)
	}
	if msg == "" {
		return fmt.Errorf("git %s: %w", what, err)
	}
	return errors.New("git " + what + ": " + msg)
}
