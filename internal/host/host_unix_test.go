//go:build !windows

package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

var fakeClaude string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "baton-host-test")
	if err != nil {
		panic(err)
	}
	fakeClaude = filepath.Join(dir, "fakeclaude")
	if out, err := exec.Command("go", "build", "-o", fakeClaude, "./testdata/fakeclaude").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// rig runs host.Run against fakeclaude with pipes standing in for the user's terminal.
type rig struct {
	t        *testing.T
	out      string // fakeclaude's record directory
	stdinW   *os.File
	stdoutR  *os.File // the user's screen: closing it is closing the terminal
	screen   *strings.Builder
	screenMu sync.Mutex
	done     chan struct{}
	code     int
	err      error
}

func startRig(t *testing.T, cfgMod func(*Config), extraEnv ...string) *rig {
	t.Helper()
	r := &rig{t: t, out: t.TempDir(), screen: &strings.Builder{}, done: make(chan struct{})}
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	r.stdinW, r.stdoutR = stdinW, stdoutR
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutR.Read(buf)
			r.screenMu.Lock()
			r.screen.Write(buf[:n])
			r.screenMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	store, _ := state.Open(filepath.Join(t.TempDir(), ".baton"), "inst-1", time.Now)
	cfg := Config{
		Claude: fakeClaude, Args: []string{"--model", "haiku"}, BatonBin: "/opt/baton/bin/baton", Store: store,
		Instance: "inst-1", Version: "test", Autocompact: "810k", Stdin: stdinR, Stdout: stdoutW,
		Env:       append(os.Environ(), append([]string{"FAKE_OUT=" + r.out, "FAKE_EXIT=7"}, extraEnv...)...),
		TypeDelay: time.Millisecond, EnterDelay: 5 * time.Millisecond,
	}
	if cfgMod != nil {
		cfgMod(&cfg)
	}
	go func() {
		defer close(r.done)
		r.code, r.err = Run(cfg)
		stdoutW.Close()
	}()
	r.waitScreen("fake-claude ready")
	return r
}

func (r *rig) waitScreen(want string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.screenMu.Lock()
		s := r.screen.String()
		r.screenMu.Unlock()
		if strings.Contains(s, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.screenMu.Lock()
	defer r.screenMu.Unlock()
	r.t.Fatalf("screen never showed %q; got %q", want, r.screen.String())
}

func (r *rig) input() string {
	b, _ := os.ReadFile(filepath.Join(r.out, "input.bin"))
	return string(b)
}

func (r *rig) start() (args []string, env map[string]string) {
	var s struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
	}
	b, _ := os.ReadFile(filepath.Join(r.out, "start.json"))
	json.Unmarshal(b, &s)
	return s.Args, s.Env
}

func (r *rig) quit() int {
	r.stdinW.Write([]byte("Q"))
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Fatal("host did not return after the child exited")
	}
	if r.err != nil {
		r.t.Fatal(r.err)
	}
	return r.code
}

func TestPassthroughSettingsAndExitCode(t *testing.T) {
	r := startRig(t, nil)
	r.stdinW.Write([]byte("hello\r"))
	r.waitScreen(`got:"hello\r"`)
	if code := r.quit(); code != 7 {
		t.Fatalf("exit code %d, want the child's 7", code)
	}
	if !strings.HasPrefix(r.input(), "hello\r") {
		t.Fatalf("child received %q", r.input())
	}
	args, env := r.start()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--autocompact 810k") || !strings.HasSuffix(joined, "--model haiku") {
		t.Fatalf("args: %v", args)
	}
	var settings map[string]any
	for i, a := range args {
		if a == "--settings" {
			json.Unmarshal([]byte(args[i+1]), &settings)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if len(hooks) < 10 || settings["statusLine"] == nil || !strings.Contains(args[1]+args[3], "Bash(baton:*)") {
		t.Fatalf("settings: %v", settings)
	}
	for k, want := range map[string]string{"BATON_HOST": "1", "BATON_INSTANCE": "inst-1", "BATON_BIN": "/opt/baton/bin/baton", "BATON_VERSION": "test"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if env["BATON_DIR"] == "" {
		t.Error("BATON_DIR not set")
	}
}

type recordingController struct {
	mu    sync.Mutex
	views []View
	typed bool
}

func (c *recordingController) Tick(v View, in Injector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.views = append(c.views, v)
	if !c.typed && v.Now.Sub(v.LastHumanKey) > 300*time.Millisecond && !v.Draft {
		c.typed = true
		in.Type("/compact", true)
	}
}

func (c *recordingController) last() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.views[len(c.views)-1]
}

func TestControllerSeesDraftsAndCanType(t *testing.T) {
	ctl := &recordingController{typed: true} // hold off typing until the draft checks are done
	r := startRig(t, func(c *Config) { c.Controller = ctl })

	r.stdinW.Write([]byte("draft"))
	time.Sleep(3 * TickInterval)
	if v := ctl.last(); !v.Draft || !v.Owner {
		t.Fatalf("after typing: %+v", v)
	}
	r.stdinW.Write([]byte("\x1b[I")) // focus report: not a keystroke
	r.stdinW.Write([]byte("\r"))
	time.Sleep(3 * TickInterval)
	if v := ctl.last(); v.Draft {
		t.Fatalf("Enter should clear the draft: %+v", v)
	}
	ctl.mu.Lock()
	ctl.typed = false
	ctl.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(r.input(), "/compact\r") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	r.quit()
	if in := r.input(); !strings.Contains(in, "draft\x1b[I\r/compact\r") {
		t.Fatalf("child received %q", in)
	}
}

// The session gets baton's own settings (the valves, a pending command after elevation), but nothing
// BATON_* it inherited: that belongs to some other session. v0.1.1 dropped both, so the hooks ran on
// default valves and an elevated session never got its pending command.
func TestTheSessionGetsBatonsOwnEnvironment(t *testing.T) {
	r := startRig(t, func(c *Config) {
		c.Env = append(c.Env, "BATON_DIR=/someone/else/.baton", "BATON_WARN_TOKENS=5")
		c.BatonEnv = []string{"BATON_WARN_TOKENS=1000", "BATON_COMPACT_CAP=810000", "BATON_PENDING=baton done P3"}
	})
	r.quit()
	_, env := r.start()
	for k, want := range map[string]string{"BATON_WARN_TOKENS": "1000", "BATON_COMPACT_CAP": "810000", "BATON_PENDING": "baton done P3"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if env["BATON_DIR"] == "/someone/else/.baton" {
		t.Error("an inherited BATON_DIR reached the session")
	}
}

func TestSecondHostRunsAsPlainPassthrough(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), ".baton"), "other", time.Now)
	store.Update(func(st *state.State) error { return state.Claim(st, "other", 1, time.Now()) })
	r := startRig(t, func(c *Config) { c.Store = store; c.BatonEnv = []string{"BATON_WARN_TOKENS=1000"} })
	r.waitScreen("runs as plain claude")
	r.quit()
	args, env := r.start()
	if strings.Contains(strings.Join(args, " "), "--settings") || env["BATON_HOST"] != "" || env["BATON_WARN_TOKENS"] != "" {
		t.Fatalf("non-owner got baton wiring: args %v env %v", args, env)
	}
}

// A session whose terminal is gone (a closed tab, a killed shell) must end, or baton and claude live on
// with nobody able to see or reach them, still holding the project. Found live: an orphaned claude
// stuck for hours in a tcsetattr waiting for output baton had stopped reading, so it never got to the
// SIGHUP baton sent it.
func TestASessionEndsWhenItsTerminalIsGone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    []string
		sighup bool // baton itself gets SIGHUP, instead of finding the screen gone
		want   int
	}{
		// It sees the hangup only if baton keeps reading its output after the screen is gone.
		{"claude exits on the hangup", []string{"FAKE_HUP=loop"}, false, 129},
		{"claude ignores the hangup", []string{"FAKE_HUP=ignore"}, false, 128 + int(syscall.SIGTERM)},
		{"claude ignores the hangup and SIGTERM", []string{"FAKE_HUP=ignore", "FAKE_TERM=ignore"}, false, 128 + int(syscall.SIGKILL)},
		{"baton gets SIGHUP", []string{"FAKE_HUP=loop"}, true, 129},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var store *state.Store
			r := startRig(t, func(c *Config) { c.HangupGrace = 200 * time.Millisecond; store = c.Store }, tc.env...)
			if tc.sighup {
				// Registered here too, so a SIGHUP that arrives before the host's own handler cannot end the
				// test binary.
				hup := make(chan os.Signal, 1)
				signal.Notify(hup, syscall.SIGHUP)
				defer signal.Stop(hup)
				time.Sleep(100 * time.Millisecond)
				syscall.Kill(os.Getpid(), syscall.SIGHUP)
			} else {
				r.stdoutR.Close()
			}
			select {
			case <-r.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the session outlived its terminal")
			}
			if r.code != tc.want {
				t.Errorf("claude ended with %d, want %d", r.code, tc.want)
			}
			if b, _ := os.ReadFile(filepath.Join(store.Dir, "events.jsonl")); !strings.Contains(string(b), `"kind":"terminal_gone"`) {
				t.Errorf("no terminal_gone event in:\n%s", b)
			}
		})
	}
}
