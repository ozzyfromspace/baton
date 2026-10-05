package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/version"
)

// MinClaudeVersion is the oldest Claude Code baton has been verified against (docs/research). Older
// versions may lack hooks baton relies on (asyncRewake, PostCompact, the Stop hook's background_tasks).
const MinClaudeVersion = "2.1.289"

func init() {
	register("setup", "check this machine is ready for baton and say what is missing", cmdSetup)
	register("doctor", "same as setup", cmdSetup)
}

func cmdSetup(_ []string, io IO) int {
	ok := true
	check := func(pass bool, good, bad string) {
		if pass {
			fmt.Fprintln(io.Out, "  ✓ "+good)
		} else {
			fmt.Fprintln(io.Out, "  ✗ "+bad)
			ok = false
		}
	}
	note := func(s string) { fmt.Fprintln(io.Out, "  · "+s) }

	exe, _ := os.Executable()
	fmt.Fprintf(io.Out, "baton %s (%s)\n", version.Version, exe)

	cv := claudeVersion()
	check(cv != "" && versionAtLeast(cv, MinClaudeVersion),
		"Claude Code "+cv,
		fmt.Sprintf("Claude Code %s found; baton needs %s or newer (update with `claude update`)", orNone(cv), MinClaudeVersion))

	root := batonRoot(io)
	binDir := filepath.Join(root, "bin")
	onPath := false
	for _, d := range filepath.SplitList(io.Env("PATH")) {
		if filepath.Clean(d) == filepath.Clean(binDir) {
			onPath = true
		}
	}
	check(onPath || hosted(io),
		"`baton` is on your shell's PATH",
		fmt.Sprintf("to start sessions with `baton`, add %s to your PATH; the init line below does it", binDir))

	shell := filepath.Base(io.Env("SHELL"))
	if shell != "bash" {
		shell = "zsh"
	}
	if elevate.Supported {
		check(elevate.ShellHookInstalled(homeDir(io), io.Env),
			"plain claude sessions can hand themselves to baton (/baton elevate)",
			fmt.Sprintf("for automatic elevation (and `baton` on your PATH), add this line to your ~/.%src and open a new terminal:\n      eval \"$(%s init %s)\"", shell, filepath.Join(binDir, "baton"), shell))
	}

	cfg := config.Load(root, io.Env)
	if cfg.NtfyTopic != "" {
		note("push notifications: ntfy topic configured (" + config.Path(root) + ")")
	} else {
		note(fmt.Sprintf("push notifications (optional): put {\"ntfy_topic\": \"<a long random name>\"} in %s and subscribe to it in the ntfy app; treat the topic like a password", config.Path(root)))
	}
	note(fmt.Sprintf("context: baton caps it with --autocompact %s and asks for a checkpoint at %g%%", orNone(cfg.Autocompact), *cfg.CheckpointPct))
	note("permissions: sessions started with `baton` allow baton's own CLI automatically; for plain sessions (elevation), add the rule Bash(baton:*) with /permissions")
	if ok {
		fmt.Fprintln(io.Out, "ready: start a session with `baton` (same arguments as `claude`), then /baton plan <goal> or /baton attach <plan>.")
	}
	return 0
}

func claudeVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		return ""
	}
	return regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out))
}

// versionAtLeast compares dotted numeric versions.
func versionAtLeast(have, want string) bool {
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < len(w); i++ {
		var a, b int
		if i < len(h) {
			a, _ = strconv.Atoi(h[i])
		}
		b, _ = strconv.Atoi(w[i])
		if a != b {
			return a > b
		}
	}
	return true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
