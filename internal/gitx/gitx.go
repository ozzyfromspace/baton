// Package gitx reads the little baton needs from git. git is optional: every function degrades to ""
// when git or the repository is missing, because the results only feed warnings.
package gitx

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Head returns the commit at HEAD for the repository containing dir, or "".
func Head(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
