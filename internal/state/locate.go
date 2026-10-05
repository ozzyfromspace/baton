package state

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Locate returns the .baton directory for a process running in cwd. BATON_DIR (set by the host for
// everything inside a hosted session) wins. Otherwise the search walks up from cwd and stops at the
// first .baton it finds, or at the root of the repository or worktree (the first directory holding
// .git), whose .baton it returns. Stopping there matters: a worktree nested inside its main checkout
// (such as .claude/worktrees/<name>) has its own state, so a session there never drives the main
// checkout's plan. Outside a repository the state lives in cwd. baton's own home (~/.baton) is never a
// project's state directory.
func Locate(cwd string, env func(string) string) string {
	if d := env("BATON_DIR"); d != "" {
		return d
	}
	home := homeBaton(env)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		cand := filepath.Join(dir, ".baton")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() && cand != home {
			return cand
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil && cand != home {
			return cand
		}
		if filepath.Dir(dir) == dir {
			return filepath.Join(cwd, ".baton")
		}
	}
}

// homeBaton is baton's own directory ($BATON_HOME, or ~/.baton), which holds binaries, config and
// elevation records rather than a project's run.
func homeBaton(env func(string) string) string {
	if r := env("BATON_HOME"); r != "" {
		return filepath.Clean(r)
	}
	h := env("HOME")
	if h == "" {
		h, _ = os.UserHomeDir()
	}
	if h == "" {
		return ""
	}
	return filepath.Join(h, ".baton")
}

// ExcludeFromGit adds ".baton/" to the repository's info/exclude so baton's state never shows up in
// `git status` and the user's .gitignore is never touched. Outside a repository it does nothing.
func ExcludeFromGit(batonDir string) error {
	root := filepath.Dir(batonDir)
	gitPath := filepath.Join(root, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return nil
	}
	gitDir := gitPath
	if !fi.IsDir() { // worktree or submodule: ".git" is a file "gitdir: <path>"
		b, err := os.ReadFile(gitPath)
		if err != nil {
			return nil
		}
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if !filepath.IsAbs(line) {
			line = filepath.Join(root, line)
		}
		gitDir = line
		if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			common := strings.TrimSpace(string(c))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			gitDir = common
		}
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	if f, err := os.Open(exclude); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t == ".baton/" || t == ".baton" || t == "/.baton/" {
				f.Close()
				return nil
			}
		}
		f.Close()
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n# baton's per-project state (added by baton)\n.baton/\n")
	return err
}
