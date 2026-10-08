package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
	"github.com/ozzyfromspace/baton/internal/version"
)

func testIO(stdin string, env map[string]string) (IO, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return IO{
		In:  strings.NewReader(stdin),
		Out: &out, Err: &errb,
		Env: func(k string) string { return env[k] },
		Now: func() time.Time { return time.Unix(0, 0) },
	}, &out, &errb
}

func TestVersionAndHelp(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		first string   // the whole first line
		has   []string // and somewhere in the output
	}{
		{[]string{"version"}, "baton " + version.Version, []string{version.Credit(), version.Homepage}},
		{[]string{"--version"}, "baton " + version.Version, []string{version.Credit(), version.Homepage}},
		{[]string{"-v"}, "baton " + version.Version, []string{version.Credit(), version.Homepage}},
		{[]string{"help"}, "baton " + version.Version + " — run multi-phase Claude Code plans unattended",
			[]string{"by Oswald Chisala · " + version.Homepage, "-h, --help", "-v, --version", "  hook  ", version.Credit()}},
		{[]string{"--help"}, "baton " + version.Version + " — run multi-phase Claude Code plans unattended", []string{"-v, --version"}},
		{[]string{"-h"}, "baton " + version.Version + " — run multi-phase Claude Code plans unattended", []string{"-v, --version"}},
	} {
		io, out, _ := testIO("", nil)
		code := Main(tc.args, io)
		first, _, _ := strings.Cut(out.String(), "\n")
		if code != 0 || first != tc.first {
			t.Errorf("%v: code %d, first line %q, want %q", tc.args, code, first, tc.first)
		}
		for _, want := range tc.has {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: no %q in:\n%s", tc.args, want, out.String())
			}
		}
	}
}

// baton version also says which baton hosts this session and which is installed: after an update they
// can differ from the binary on the PATH until the session restarts.
func TestVersionTellsTheSessionAndInstalledVersions(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS != "windows" {
		os.MkdirAll(filepath.Join(home, "bin", "v0.3.2"), 0o755)
		os.WriteFile(filepath.Join(home, "bin", "v0.3.2", "baton"), []byte("#!/bin/sh\n"), 0o755)
		os.Symlink(filepath.Join("v0.3.2", "baton"), filepath.Join(home, "bin", "baton"))
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"BATON_HOST": "1", "BATON_VERSION": "v0.3.1"}, "this session: hosted by baton v0.3.1"},
		{map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"}, "this session: plain Claude Code, not hosted by baton (/baton start hands it over)"},
	} {
		tc.env["BATON_HOME"] = home
		io, out, _ := testIO("", tc.env)
		if code := Main([]string{"version"}, io); code != 0 || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%v: code %d\n%s", tc.env, code, out.String())
		}
		if runtime.GOOS != "windows" && !strings.Contains(out.String(), "installed: baton v0.3.2") {
			t.Errorf("no installed version:\n%s", out.String())
		}
	}
	io, out, _ := testIO("", map[string]string{"BATON_HOME": t.TempDir()})
	if Main([]string{"version"}, io); strings.Contains(out.String(), "this session") || strings.Contains(out.String(), "installed") {
		t.Errorf("from a shell, with nothing installed:\n%s", out.String())
	}
}

// Inside a claude session, `baton <anything that is not a command>` must not start another claude in
// the Bash tool: a mistyped or retired command name would otherwise be taken for a prompt.
func TestNoClaudeStartsInsideASession(t *testing.T) {
	for _, args := range [][]string{{"elevate", "Begin the plan."}, {"stauts"}, {}, {"--continue"}} {
		io, _, errb := testIO("", map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1", "BATON_CLAUDE": "/nonexistent/claude"})
		if code := Main(args, io); code == 0 || !strings.Contains(errb.String(), "this is already one. To hand it to baton, run /baton start") {
			t.Errorf("%v: code %d, %q", args, code, errb.String())
		}
	}
	io, _, errb := testIO("", map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"})
	if Main([]string{"elevate"}, io); !strings.Contains(errb.String(), `"elevate" is not a baton command`) {
		t.Errorf("a retired name: %q", errb.String())
	}
	// A hosted session says the same, and that it is hosted already.
	io, _, errb = testIO("", map[string]string{"BATON_HOST": "1", "CLAUDE_CODE_SESSION_ID": "sess-1", "BATON_CLAUDE": "/nonexistent/claude"})
	if code := Main([]string{"resume"}, io); code == 0 || !strings.Contains(errb.String(), `"resume" is not a baton command`) ||
		!strings.Contains(errb.String(), "this is already one, hosted by baton. `baton help` lists baton's commands.") {
		t.Errorf("a retired name in a hosted session: code %d, %q", code, errb.String())
	}
}

func TestStartInAHostedSessionDoesNothing(t *testing.T) {
	io, out, _ := testIO("", map[string]string{"BATON_HOST": "1", "CLAUDE_CODE_SESSION_ID": "sess-1"})
	if code := Main([]string{"start"}, io); code != 0 || !strings.Contains(out.String(), "already hosted by baton") {
		t.Fatalf("code %d: %s", code, out.String())
	}
}

// A malformed hook invocation must never exit 2 (which Claude Code reads as "block").
func TestHookArgumentProblemsFailOpen(t *testing.T) {
	hosted := map[string]string{"BATON_HOST": "1"}
	for _, args := range [][]string{{"hook"}, {"hook", "--bogus"}, {"hook", "Stop", "--bogus"}, {"hook", "NotAnEvent"}} {
		io, out, _ := testIO(`{}`, hosted)
		if code := Main(args, io); code != 0 || out.Len() != 0 {
			t.Errorf("%v: code %d stdout %q", args, code, out.String())
		}
	}
}

func TestHookPanicFailsOpenThroughCLI(t *testing.T) {
	old := hookHandlers
	defer func() { hookHandlers = old }()
	hookHandlers = map[string]hooks.Handler{"Stop": func(hooks.Context) (hooks.Result, error) { panic("boom") }}
	io, out, _ := testIO(`{}`, map[string]string{"BATON_HOST": "1"})
	if code := Main([]string{"hook", "Stop"}, io); code != 0 || out.Len() != 0 {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{{"2.1.289", "2.1.289", true}, {"2.1.300", "2.1.289", true}, {"2.2.0", "2.1.289", true}, {"2.1.288", "2.1.289", false}, {"1.9.999", "2.1.289", false}} {
		if versionAtLeast(c.have, c.want) != c.ok {
			t.Errorf("%s >= %s", c.have, c.want)
		}
	}
}

func TestContextSettings(t *testing.T) {
	pct := func(f float64) *float64 { return &f }
	cfg := func(autocompact string) config.Config {
		return config.Config{Autocompact: autocompact, CheckpointPct: pct(60), WarnPct: pct(90)}
	}
	for _, c := range []struct {
		name, autocompact string
		args              []string
		env               map[string]string
		flag              string // what baton passes as --autocompact
		cap               int
		err               bool
	}{
		{name: "default", autocompact: "810k", flag: "810k", cap: 810_000},
		{name: "off", autocompact: "", flag: "", cap: 0},
		{name: "auto", autocompact: "auto", flag: "auto", cap: 0},
		{name: "invalid config", autocompact: "50k", err: true},
		{name: "the user's own flag wins", autocompact: "810k", args: []string{"--model", "opus", "--autocompact", "500k"}, flag: "", cap: 500_000},
		{name: "the user's own flag, = form", autocompact: "810k", args: []string{"--autocompact=600k"}, flag: "", cap: 600_000},
		{name: "a bad flag is claude's to report", autocompact: "810k", args: []string{"--autocompact", "5"}, flag: "", cap: 0},
		{name: "after --, not a flag", autocompact: "810k", args: []string{"--", "--autocompact", "500k"}, flag: "810k", cap: 810_000},
		{name: "Claude Code's env var wins", autocompact: "810k", env: map[string]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "300000"}, flag: "810k", cap: 300_000},
		{name: "test warning override", autocompact: "810k", env: map[string]string{"BATON_WARN_TOKENS": "5000"}, flag: "810k", cap: 810_000},
	} {
		flag, vs, err := contextSettings(cfg(c.autocompact), c.args, func(k string) string { return c.env[k] })
		if c.err {
			if err == nil {
				t.Errorf("%s: no error", c.name)
			}
			continue
		}
		want := valve.Settings{Cap: c.cap, CheckpointPct: 60, WarnPct: 90}
		if c.env["BATON_WARN_TOKENS"] != "" {
			want.WarnTokens = 5000
		}
		if err != nil || flag != c.flag || vs != want {
			t.Errorf("%s: flag %q settings %+v err %v; want %q %+v", c.name, flag, vs, err, c.flag, want)
		}
	}
}

// A hook that fails open leaves a trace in the event log, where a stall gets diagnosed.
func TestHookFailuresAreRecorded(t *testing.T) {
	old := hookHandlers
	defer func() { hookHandlers = old }()
	hookHandlers = map[string]hooks.Handler{"Stop": func(hooks.Context) (hooks.Result, error) {
		return hooks.Result{}, fmt.Errorf("baton state is locked by another process")
	}}
	dir := filepath.Join(t.TempDir(), ".baton")
	p, _ := state.OpenProject(dir, nil)
	run, err := p.Bind(state.Binding{Session: "sess", Instance: "i"}) // as the session's first hook did
	if err != nil {
		t.Fatal(err)
	}
	io, _, _ := testIO(`{}`, map[string]string{"BATON_HOST": "1", "BATON_DIR": dir, "BATON_INSTANCE": "i", "CLAUDE_CODE_SESSION_ID": "sess"})
	if code := Main([]string{"hook", "Stop"}, io); code != 0 {
		t.Fatalf("code %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(run.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"hook_failed"`) || !strings.Contains(string(b), "locked by another process") {
		t.Fatalf("events: %s", b)
	}
}

// baton binds a session to its run at launch whenever it can know the session id: one it chooses for a
// new session, or the one being resumed.
func TestTheSessionIsKnownAtLaunchWhenItCanBe(t *testing.T) {
	const id = "16c4eeb9-22e9-4156-b87a-5f6dba0c9747"
	for _, c := range []struct {
		args []string
		want string // "new": a fresh id, passed as --session-id
	}{
		{nil, "new"},
		{[]string{"--model", "haiku", "fix the bug"}, "new"},
		{[]string{"--resume", id}, id},
		{[]string{"-r", id, "--model", "x"}, id},
		{[]string{"--resume=" + id}, id},
		{[]string{"--session-id", id}, id},
		{[]string{"--resume"}, ""},
		{[]string{"--resume", "the auth refactor"}, ""},
		{[]string{"--continue"}, ""},
		{[]string{"-c", "--model", "x"}, ""},
		{[]string{"--resume", id, "--fork-session"}, ""},
		{[]string{"-p", "hi"}, ""},
	} {
		got, args := sessionOf(c.args)
		added := len(args) == len(c.args)+2 && args[len(args)-2] == "--session-id" && args[len(args)-1] == got
		switch {
		case c.want == "new" && (!looksLikeSessionID(got) || !added):
			t.Errorf("%v: got %q, args %v", c.args, got, args)
		case c.want != "new" && (got != c.want || len(args) != len(c.args)):
			t.Errorf("%v: got %q, args %v; want %q", c.args, got, args, c.want)
		}
	}
	a, _ := sessionOf(nil)
	b, _ := sessionOf(nil)
	if a == b {
		t.Error("two new sessions got the same id")
	}
}

// After `baton exit`, the shell's prompt hook resumes the conversation as plain claude: no baton host,
// none of baton's environment.
func TestRelaunchResumesAPlainSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in claude is a shell script")
	}
	home, dir, out := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "args")
	fake := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" > "+out+"\nenv | grep '^BATON_' >> "+out+"\nexit 0\n"), 0o755)
	now := time.Now()
	elevate.Save(home, elevate.Record{SessionID: "sess-1", Dir: dir, TTY: "ttys999", Args: []string{"--model", "opus"}, Plain: true, Created: now, Stopped: now})
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	io, stdout, _ := testIO("", map[string]string{"BATON_HOME": home, "BATON_CLAUDE": fake})
	if code := Main([]string{"relaunch", "--tty", "ttys999"}, io); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(out)
	if strings.TrimSpace(string(b)) != "--resume sess-1 --model opus" || !strings.Contains(stdout.String(), "as plain Claude Code") {
		t.Fatalf("ran %q; said %q", b, stdout.String())
	}
}

// A command run inside a hosted session hands itself to the host's binary when another baton ran it
// (the newest, first on the PATH after an update), and never to itself.
func TestHostBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no exec on Windows")
	}
	exe, _ := os.Executable()
	other := filepath.Join(t.TempDir(), "baton")
	os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755)
	link := filepath.Join(t.TempDir(), "baton")
	os.Symlink(exe, link)
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"BATON_HOST": "1", "BATON_BIN": other}, other},
		{map[string]string{"BATON_HOST": "1", "BATON_BIN": exe}, ""},
		{map[string]string{"BATON_HOST": "1", "BATON_BIN": link}, ""}, // itself, through a link
		{map[string]string{"BATON_BIN": other}, ""},                   // not hosted
		{map[string]string{"BATON_HOST": "1", "BATON_BIN": filepath.Join(t.TempDir(), "gone")}, ""},
		{map[string]string{"BATON_HOST": "1"}, ""},
	} {
		if got := HostBinary(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.env, got, tc.want)
		}
	}
}
