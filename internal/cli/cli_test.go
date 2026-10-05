package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/valve"
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
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		io, out, _ := testIO("", nil)
		if code := Main(args, io); code != 0 || !strings.HasPrefix(out.String(), "baton ") {
			t.Errorf("%v: code %d out %q", args, code, out.String())
		}
	}
	io, out, _ := testIO("", nil)
	if code := Main([]string{"help"}, io); code != 0 || !strings.Contains(out.String(), "hook") {
		t.Errorf("help: code %d out %q", code, out.String())
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
	dir := t.TempDir()
	io, _, _ := testIO(`{}`, map[string]string{"BATON_HOST": "1", "BATON_DIR": dir, "BATON_INSTANCE": "i"})
	if code := Main([]string{"hook", "Stop"}, io); code != 0 {
		t.Fatalf("code %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(b), `"kind":"hook_failed"`) || !strings.Contains(string(b), "locked by another process") {
		t.Fatalf("events: %s", b)
	}
}
