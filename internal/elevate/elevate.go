// Package elevate hands a plain `claude` session over to baton without losing the conversation.
//
// A running process cannot be moved into another terminal, but a conversation can be resumed. So:
//  1. inside the plain session, `baton elevate` records the session (id, directory, terminal, the flags
//     claude was started with, and what to do next) in ~/.baton/elevate/<terminal>.json;
//  2. when that turn ends, baton's plugin Stop hook stops claude cleanly (SIGTERM);
//  3. the shell's prompt hook (`eval "$(baton init zsh)"`) sees the record for its own terminal and runs
//     `baton relaunch`, which resumes the same session under baton;
//  4. a SessionStart hook hands the model the pending command.
//
// Records are keyed by terminal so only the shell that ran claude picks one up, used once, and ignored if
// claude was stopped more than RelaunchWindow ago.
package elevate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RelaunchWindow is how soon after claude was stopped the shell must pick up the record.
const RelaunchWindow = 60 * time.Second

// Record is ~/.baton/elevate/<terminal>.json.
type Record struct {
	SessionID string   `json:"session_id"`
	Dir       string   `json:"dir"`
	ClaudePID int      `json:"claude_pid"`
	TTY       string   `json:"tty"`
	Args      []string `json:"args"`    // claude flags worth keeping across the relaunch
	Pending   string   `json:"pending"` // what the model should do once hosted
	// Plain: the human is leaving baton (`baton exit`), so the shell resumes the session as plain claude.
	Plain   bool      `json:"plain,omitempty"`
	Created time.Time `json:"created"`
	Stopped time.Time `json:"stopped,omitzero"` // set by the Stop hook just before it stops claude
}

// Dir is where records live, under baton's root directory (~/.baton, or $BATON_HOME).
func Dir(root string) string { return filepath.Join(root, "elevate") }

// Key turns a terminal device ("/dev/ttys003", "ttys003", "/dev/pts/3", "pts/3") into a file name.
func Key(tty string) string {
	tty = strings.TrimPrefix(strings.TrimSpace(tty), "/dev/")
	return strings.ReplaceAll(tty, "/", "-")
}

// Path is the record file for a terminal.
func Path(root, tty string) string { return filepath.Join(Dir(root), Key(tty)+".json") }

// Save writes the record for its terminal.
func Save(root string, r Record) error {
	if err := os.MkdirAll(Dir(root), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(root, r.TTY) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(root, r.TTY))
}

// Load reads the record for a terminal.
func Load(root, tty string) (Record, error) {
	var r Record
	b, err := os.ReadFile(Path(root, tty))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// FindSession returns the record for a session id, if any terminal has one.
func FindSession(root, sessionID string) (Record, bool) {
	entries, _ := os.ReadDir(Dir(root))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r Record
		if b, err := os.ReadFile(filepath.Join(Dir(root), e.Name())); err == nil && json.Unmarshal(b, &r) == nil && r.SessionID == sessionID {
			return r, true
		}
	}
	return Record{}, false
}

// ErrNothingToRelaunch means there is no usable record for this terminal.
var ErrNothingToRelaunch = errors.New("nothing to relaunch")

// Take loads the record for a terminal and consumes it (it is used at most once). It refuses a record
// whose claude was never stopped, or was stopped too long ago.
func Take(root, tty string, now time.Time) (Record, error) {
	r, err := Load(root, tty)
	if err != nil {
		return r, ErrNothingToRelaunch
	}
	if r.Stopped.IsZero() {
		return r, ErrNothingToRelaunch // claude is still running (or elevation was refused); keep waiting
	}
	os.Remove(Path(root, tty))
	if now.Sub(r.Stopped) > RelaunchWindow {
		return r, fmt.Errorf("the elevation record for %s is stale (claude stopped %s ago)", r.TTY, now.Sub(r.Stopped).Round(time.Second))
	}
	return r, nil
}

// keepFlags are the claude flags that matter across a relaunch, with whether each takes a value.
var keepFlags = map[string]bool{
	"--model": true, "--permission-mode": true, "--plugin-dir": true, "--add-dir": true, "--agent": true,
	"--effort": true, "--fallback-model": true, "--mcp-config": true,
	"--dangerously-skip-permissions": false, "--verbose": false, "--strict-mcp-config": false,
}

// userFlags are flags worth keeping from the arguments a human gave baton itself. They are not kept from
// a claude process's command line, where baton may have put them (its own --settings and --autocompact),
// and a session leaving baton must not take those along.
var userFlags = map[string]bool{
	"--autocompact": true, "--settings": true, "--setting-sources": true, "--append-system-prompt": true, "--system-prompt": true,
}

// KeepArgs picks the flags worth keeping out of claude's original command line. Prompts and
// session-selection flags are dropped: the relaunch resumes this session explicitly.
func KeepArgs(argv []string) []string { return keepArgs(argv, keepFlags) }

// KeepUserArgs is KeepArgs for the claude arguments a human gave baton, kept when baton restarts the
// session on a newer version of itself.
func KeepUserArgs(argv []string) []string { return keepArgs(argv, keepFlags, userFlags) }

func keepArgs(argv []string, sets ...map[string]bool) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		name, _, hasEq := strings.Cut(a, "=")
		var takesValue, ok bool
		for _, set := range sets {
			if v, in := set[name]; in {
				takesValue, ok = v, true
			}
		}
		if !ok {
			continue
		}
		out = append(out, a)
		if takesValue && !hasEq && i+1 < len(argv) {
			i++
			out = append(out, argv[i])
		}
	}
	return out
}

// Init returns the shell code for `eval "$(baton init <shell>)"`: it puts ~/.baton/bin on PATH and adds a
// prompt hook that relaunches an elevated session in the terminal it came from. The hook only runs the
// baton binary when an elevation record exists, so it costs nothing on an ordinary prompt.
func Init(shell, batonBin string) (string, error) {
	switch shell {
	case "zsh":
		return fmt.Sprintf(`# baton: relaunch elevated claude sessions under baton (https://github.com/ozzyfromspace/baton)
case ":$PATH:" in *":${BATON_HOME:-$HOME/.baton}/bin:"*) ;; *) export PATH="${BATON_HOME:-$HOME/.baton}/bin:$PATH" ;; esac
_baton_relaunch() {
  local d="${BATON_HOME:-$HOME/.baton}/elevate"
  [[ -d $d ]] || return
  local -a recs; recs=("$d"/*.json(N))
  (( ${#recs} )) || return
  %s relaunch --tty "$TTY"
}
typeset -ga precmd_functions
(( ${precmd_functions[(I)_baton_relaunch]} )) || precmd_functions+=(_baton_relaunch)
`, shQuote(batonBin)), nil
	case "bash":
		return fmt.Sprintf(`# baton: relaunch elevated claude sessions under baton (https://github.com/ozzyfromspace/baton)
case ":$PATH:" in *":${BATON_HOME:-$HOME/.baton}/bin:"*) ;; *) export PATH="${BATON_HOME:-$HOME/.baton}/bin:$PATH" ;; esac
_baton_relaunch() {
  local d="${BATON_HOME:-$HOME/.baton}/elevate"
  [ -d "$d" ] || return
  compgen -G "$d/*.json" >/dev/null || return
  %s relaunch --tty "$(tty)"
}
case ";${PROMPT_COMMAND:-};" in *";_baton_relaunch;"*) ;; *) PROMPT_COMMAND="_baton_relaunch${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;; esac
`, shQuote(batonBin)), nil
	default:
		return "", fmt.Errorf("baton init supports zsh and bash (got %q)", shell)
	}
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ShellHookInstalled guesses whether the user's shell runs baton's init line, by looking for it in the
// usual startup files (BATON_SHELL_HOOK=1 asserts it for setups this cannot see). Elevation without it
// would stop claude with nothing to relaunch it.
func ShellHookInstalled(home string, env func(string) string) bool {
	if env("BATON_SHELL_HOOK") == "1" {
		return true
	}
	for _, f := range []string{".zshrc", ".zprofile", ".bashrc", ".bash_profile", ".profile"} {
		if b, err := os.ReadFile(filepath.Join(home, f)); err == nil && strings.Contains(string(b), "baton init") {
			return true
		}
	}
	return false
}
