//go:build !windows

package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	t            *testing.T
	out          string // fakeclaude's record directory
	stdinW       *os.File
	screen       *strings.Builder
	screenMu     sync.Mutex
	done         chan struct{}
	code         int
	err          error
}

func startRig(t *testing.T, cfgMod func(*Config), extraEnv ...string) *rig {
	t.Helper()
	r := &rig{t: t, out: t.TempDir(), screen: &strings.Builder{}, done: make(chan struct{})}
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	r.stdinW = stdinW
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
		Instance: "inst-1", Version: "test", Autocompact: "400k", Stdin: stdinR, Stdout: stdoutW,
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
	if !strings.Contains(joined, "--autocompact 400k") || !strings.HasSuffix(joined, "--model haiku") {
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

func TestSecondHostRunsAsPlainPassthrough(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), ".baton"), "other", time.Now)
	store.Update(func(st *state.State) error { return state.Claim(st, "other", 1, time.Now()) })
	r := startRig(t, func(c *Config) { c.Store = store })
	r.waitScreen("runs as plain claude")
	r.quit()
	args, env := r.start()
	if strings.Contains(strings.Join(args, " "), "--settings") || env["BATON_HOST"] != "" {
		t.Fatalf("non-owner got baton wiring: args %v env %v", args, env)
	}
}
