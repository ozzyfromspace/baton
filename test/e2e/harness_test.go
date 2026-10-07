//go:build !windows

// Package e2e drives real claude sessions through baton, the way a human at a terminal would. The tests
// cost a few cents each and only run with BATON_E2E=1 (make e2e). They assert on the runs' events.jsonl
// and files the scenario writes, never on screen contents, which are only kept for debugging.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cpty "github.com/creack/pty"
)

var batonBin string // built once in TestMain

func TestMain(m *testing.M) {
	if os.Getenv("BATON_E2E") != "1" {
		fmt.Println("e2e: skipped (set BATON_E2E=1 to run real claude sessions; costs a few cents)")
		os.Exit(0)
	}
	// Run from inside a baton-hosted session, this process inherits that session's BATON_DIR, and every
	// baton command a test runs would act on the developer's live run. Nothing here may see the
	// developer's baton or keyring: baton's home and gpg's are throwaway directories for every command.
	// Nor may a baton command a test runs from here (an attach from the "shell") think it is inside the
	// developer's Claude Code session, whose id it would take for its own.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BATON_") && k != "BATON_E2E" && k != "BATON_E2E_SONNET" || strings.HasPrefix(k, "CLAUDE") {
			os.Unsetenv(k)
		}
	}
	scratch, err := os.MkdirTemp("", "baton-e2e")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(scratch)
	for k, sub := range map[string]string{"BATON_HOME": "home", "GNUPGHOME": "gnupg"} {
		p := filepath.Join(scratch, sub)
		if err := os.Mkdir(p, 0o700); err != nil {
			panic(err)
		}
		os.Setenv(k, p)
	}
	dir, err := os.MkdirTemp("", "baton-e2e-bin")
	if err != nil {
		panic(err)
	}
	batonBin = filepath.Join(dir, "baton")
	if out, err := exec.Command("go", "build", "-o", batonBin, "../../cmd/baton").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.RemoveAll(scratch)
	os.Exit(code)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

// Session is one baton-hosted claude in a pseudo-terminal, with the test acting as the terminal.
type Session struct {
	t       *testing.T
	Dir     string
	cmd     *exec.Cmd
	pty     *os.File
	mu      sync.Mutex
	screen  bytes.Buffer
	lastOut time.Time
	exited  chan struct{}
}

// NewProject makes a throwaway git repository to run a session in. It has its own identity and does
// not sign: the developer's global git config may sign every commit, and no test may reach their
// keyring (GNUPGHOME is a throwaway directory).
func NewProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, "init", "-q")
	Git(t, dir, "config", "user.name", "baton e2e")
	Git(t, dir, "config", "user.email", "e2e@baton.invalid")
	Git(t, dir, "config", "commit.gpgsign", "false")
	Git(t, dir, "config", "tag.gpgsign", "false")
	return dir
}

// NewSignedProject makes a git repository whose history is signed, like the one in the incident behind
// graduated escalation (docs/escalation.md): commit.gpgsign is on, and gpg is a stand-in that signs
// without any keyring. breakSigning makes every signature from then on time out, as the real gpg did
// once the machine had restarted and its agent had lost the passphrase. No real gpg is ever run.
func NewSignedProject(t *testing.T) (dir string, breakSigning func()) {
	t.Helper()
	dir, tools := t.TempDir(), t.TempDir()
	gpg := func(name, body string) string {
		p := filepath.Join(tools, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	signs := gpg("gpg-signs", "cat >/dev/null\n"+
		"echo '[GNUPG:] SIG_CREATED D 1 8 00 1700000000 5A1E5A1E5A1E5A1E' >&2\n"+
		"printf -- '-----BEGIN PGP SIGNATURE-----\\n\\niQEzBAABCAAdFiEE\\n-----END PGP SIGNATURE-----\\n'\n")
	timesOut := gpg("gpg-times-out", "cat >/dev/null 2>&1\nsleep 4\n"+
		"echo 'gpg: signing failed: Operation timed out' >&2\necho '[GNUPG:] FAILURE sign 67108949' >&2\nexit 2\n")
	Git(t, dir, "init", "-q")
	Git(t, dir, "config", "user.name", "baton e2e")
	Git(t, dir, "config", "user.email", "e2e@baton.invalid")
	Git(t, dir, "config", "commit.gpgsign", "true")
	Git(t, dir, "config", "user.signingkey", "5A1E5A1E5A1E5A1E")
	Git(t, dir, "config", "gpg.program", signs)
	for _, f := range []struct{ name, text, msg string }{
		{"README.md", "# Greeter\n\nSays hello and goodbye.\n", "chore: start the greeter"},
		{"NOTES.md", "Signed history: every commit here is signed.\n", "docs: note that history is signed"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.text), 0o644); err != nil {
			t.Fatal(err)
		}
		Git(t, dir, "add", f.name)
		Git(t, dir, "commit", "-q", "-m", f.msg)
	}
	return dir, func() { Git(t, dir, "config", "gpg.program", timesOut) }
}

// Git runs git in dir and returns its output, failing the test if git fails.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// NewPlainProject makes a throwaway folder that is not a git repository. baton runs a plan there the same
// way, with nothing that needs git.
func NewPlainProject(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// Start runs `baton <args…>` in dir. Inherited CLAUDE* variables are removed (a claude started from
// inside another Claude Code session would otherwise not save transcripts), and the directory holding
// the baton binary is put first on PATH, as the plugin's bin/ is in real use.
func Start(t *testing.T, dir string, args ...string) *Session {
	t.Helper()
	return StartEnv(t, dir, nil, args...)
}

// StartEnv is Start with extra environment variables for baton (e.g. BATON_CHECKPOINT_PCT).
func StartEnv(t *testing.T, dir string, env []string, args ...string) *Session {
	t.Helper()
	return StartProgram(t, dir, env, batonBin, args...)
}

// StartProgram runs any program (a shell, plain claude) the way Start runs baton. Every session gets its
// own baton home and gpg home, so the developer's config (an ntfy topic, settings a test does not
// expect) and keyring never leak in; env can still set either.
func StartProgram(t *testing.T, dir string, env []string, prog string, args ...string) *Session {
	t.Helper()
	cmd := exec.Command(prog, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "BATON_") && !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "GNUPGHOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "PATH="+filepath.Dir(batonBin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BATON_HOME="+t.TempDir(), "GNUPGHOME="+t.TempDir(),
		"BATON_NOTIFY_DESKTOP=0", "BATON_NTFY_TOPIC=") // tests never notify the developer
	cmd.Env = append(cmd.Env, env...) // later entries win
	p, err := cpty.StartWithSize(cmd, &cpty.Winsize{Rows: 50, Cols: 160})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{t: t, Dir: dir, cmd: cmd, pty: p, lastOut: time.Now(), exited: make(chan struct{})}
	go s.pump()
	go func() { cmd.Wait(); close(s.exited) }()
	t.Cleanup(s.Close)
	return s
}

func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.screen.Write(ansi.ReplaceAll(buf[:n], nil))
			s.lastOut = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Screen is everything shown so far with escape sequences removed (spaces too, since the TUI positions
// words with cursor moves). For debugging and coarse waits only.
func (s *Session) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.ReplaceAll(s.screen.String(), " ", "")
}

// Until polls cond until it holds or timeout passes.
func (s *Session) Until(what string, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case <-s.exited:
			s.t.Logf("session exited while waiting for %s", what)
			return cond()
		case <-time.After(250 * time.Millisecond):
		}
	}
	s.t.Logf("timed out waiting for %s", what)
	return false
}

// WaitScreen waits for text (spaces ignored) to appear on screen.
func (s *Session) WaitScreen(text string, timeout time.Duration) bool {
	want := strings.ReplaceAll(text, " ", "")
	return s.Until("screen "+text, timeout, func() bool { return strings.Contains(s.Screen(), want) })
}

// WaitQuiet waits until the screen has been still for d.
func (s *Session) WaitQuiet(d, timeout time.Duration) bool {
	return s.Until("quiet", timeout, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return time.Since(s.lastOut) >= d
	})
}

// Trust answers Claude Code's folder-trust prompt if it appears.
func (s *Session) Trust() {
	if s.WaitScreen("trust this folder", 20*time.Second) {
		time.Sleep(800 * time.Millisecond)
		s.pty.Write([]byte("\x1b[B"))
		time.Sleep(400 * time.Millisecond)
		s.pty.Write([]byte("\r"))
	}
}

// Type types text like a person, then presses Enter.
func (s *Session) Type(text string) {
	for _, r := range text {
		s.pty.Write([]byte(string(r)))
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	s.pty.Write([]byte("\r"))
}

// Key sends raw keys, such as "\x1b[Z" (shift+tab), with no Enter.
func (s *Session) Key(keys string) {
	s.pty.Write([]byte(keys))
	time.Sleep(400 * time.Millisecond)
}

// PlanMode switches the session into plan mode with shift+tab, as a person would.
func (s *Session) PlanMode() bool {
	for i := 0; i < 5; i++ {
		if strings.Contains(s.Screen(), "planmodeon") {
			return true
		}
		s.Key("\x1b[Z")
		time.Sleep(800 * time.Millisecond)
	}
	return strings.Contains(s.Screen(), "planmodeon")
}

// AutoApprove plays a human who approves every dialog Claude Code opens (permission prompts, plan-mode
// entry and plan approval). Haiku has no auto mode, so tests that let the model act freely need it.
func (s *Session) AutoApprove() {
	go func() {
		approved := 0
		for {
			select {
			case <-s.exited:
				return
			case <-time.After(500 * time.Millisecond):
			}
			opens := 0
			for _, e := range s.Events() {
				if e["kind"] == "dialog_open" {
					opens++
				}
			}
			if opens > approved {
				time.Sleep(1500 * time.Millisecond)
				s.pty.Write([]byte("\r"))
				approved = opens
			}
		}
	}()
}

// RemovePlansAfter deletes plan files the session wrote into ~/.claude/plans (plan mode writes there),
// so tests never leave plans behind in the developer's real plans folder.
func (s *Session) RemovePlansAfter() {
	home, _ := os.UserHomeDir()
	plans := filepath.Join(home, ".claude", "plans")
	s.t.Cleanup(func() {
		for _, e := range s.Events() {
			if p, _ := e["plan"].(string); (e["kind"] == "attached" || e["kind"] == "plan_not_attached") && filepath.Dir(p) == plans {
				os.Remove(p)
			}
		}
	})
}

// Events reads the event logs of every run in the project (.baton/runs/*/events.jsonl), oldest first.
// A test with one session sees that session's run.
func (s *Session) Events() []map[string]any {
	logs, _ := filepath.Glob(filepath.Join(s.Dir, ".baton", "runs", "*", "events.jsonl"))
	var out []map[string]any
	for _, path := range logs {
		out = append(out, readEvents(path)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return eventTime(out[i]).Before(eventTime(out[j])) })
	return out
}

// OwnEvents reads the event log of this session's run alone: the run whose host is this baton process.
func (s *Session) OwnEvents() []map[string]any {
	logs, _ := filepath.Glob(filepath.Join(s.Dir, ".baton", "runs", "*", "events.jsonl"))
	for _, path := range logs {
		evs := readEvents(path)
		for _, e := range evs {
			if pid, _ := e["pid"].(float64); e["kind"] == "host_started" && int(pid) == s.cmd.Process.Pid {
				return evs
			}
		}
	}
	return nil
}

func readEvents(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func eventTime(e map[string]any) time.Time {
	ts, _ := e["ts"].(string)
	t, _ := time.Parse(time.RFC3339Nano, ts)
	return t
}

// WaitEvent waits for an event of the given kind and returns it.
func (s *Session) WaitEvent(kind string, timeout time.Duration) map[string]any {
	var found map[string]any
	s.Until("event "+kind, timeout, func() bool {
		for _, e := range s.Events() {
			if e["kind"] == kind {
				found = e
				return true
			}
		}
		return false
	})
	return found
}

// Close ends the session and keeps its screen next to the project for debugging failures.
func (s *Session) Close() {
	select {
	case <-s.exited:
	default:
		s.cmd.Process.Signal(os.Interrupt)
		select {
		case <-s.exited:
		case <-time.After(3 * time.Second):
			s.cmd.Process.Kill()
		}
	}
	s.pty.Close()
	if s.t.Failed() {
		s.t.Logf("screen tail:\n%s", tail(s.Screen(), 3000))
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
