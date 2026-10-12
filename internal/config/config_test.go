package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/machine"
)

func TestLoadFileEnvAndDefaults(t *testing.T) {
	root := t.TempDir()
	c := Load(root, func(string) string { return "" })
	if c.Autocompact != DefaultAutocompact || *c.CheckpointPct != DefaultCheckpointPct || *c.WarnPct != DefaultWarnPct || c.NtfyServer != DefaultNtfyServer || !c.DesktopOn() || !c.Restarts() {
		t.Fatalf("defaults: %+v", c)
	}
	os.WriteFile(Path(root), []byte(`{"ntfy_topic":"file","details":true,"autocompact":"off","checkpoint_pct":50,"warn_pct":0,"auto_restart":false}`), 0o644)
	c = Load(root, func(string) string { return "" })
	if c.NtfyTopic != "file" || !c.Details || c.Autocompact != "" || *c.CheckpointPct != 50 || *c.WarnPct != 0 || c.Restarts() {
		t.Fatalf("file: %+v", c)
	}
	env := map[string]string{"BATON_NTFY_TOPIC": "env", "BATON_NOTIFY_DESKTOP": "0", "BATON_AUTOCOMPACT": "600k", "BATON_CHECKPOINT_PCT": "0", "BATON_WARN_PCT": "80", "BATON_AUTO_RESTART": "1"}
	c = Load(root, func(k string) string { return env[k] })
	if c.NtfyTopic != "env" || c.DesktopOn() || c.Autocompact != "600k" || *c.CheckpointPct != 0 || *c.WarnPct != 80 || !c.Restarts() {
		t.Fatalf("env: %+v", c)
	}
}

func TestRoot(t *testing.T) {
	env := map[string]string{"HOME": "/home/x"}
	if got := Root(func(k string) string { return env[k] }); got != filepath.Join("/home/x", ".baton") {
		t.Fatalf("Root = %q", got)
	}
	env["BATON_HOME"] = "/elsewhere"
	if got := Root(func(k string) string { return env[k] }); got != "/elsewhere" {
		t.Fatalf("Root with BATON_HOME = %q", got)
	}
}

// The parser must agree with Claude Code's, which refuses to start on a value it cannot read.
func TestAutocompactTokens(t *testing.T) {
	for _, c := range []struct {
		in     string
		tokens int
		ok     bool
	}{
		{"810k", 810_000, true}, {"810K", 810_000, true}, {" 1m ", 1_000_000, true}, {"0.5M", 500_000, true},
		{"200000", 200_000, true}, {"200", 200_000, true}, {"auto", 0, true},
		{"50k", 0, false}, {"2m", 0, false}, {"99999", 0, false}, {"lots", 0, false}, {"", 0, false},
	} {
		if tokens, ok := AutocompactTokens(c.in); tokens != c.tokens || ok != c.ok {
			t.Errorf("%q: %d %v, want %d %v", c.in, tokens, ok, c.tokens, c.ok)
		}
	}
}

func TestEscalationSettings(t *testing.T) {
	root := t.TempDir()
	load := func(file string, env map[string]string) (Escalation, error) {
		os.WriteFile(Path(root), []byte(file), 0o644)
		return Load(root, func(k string) string { return env[k] }).Escalation()
	}
	for _, c := range []struct {
		name string
		file string
		env  map[string]string
		want Escalation
		err  string
	}{
		{"defaults", `{}`, nil, Escalation{5 * time.Minute, 5}, ""},
		{"file", `{"escalation_timeout":"90s","max_auto_decisions":0}`, nil, Escalation{90 * time.Second, 0}, ""},
		{"file bounds", `{"escalation_timeout":"2h"}`, nil, Escalation{2 * time.Hour, 5}, ""},
		{"env wins", `{"escalation_timeout":"10m","max_auto_decisions":3}`, map[string]string{"BATON_ESCALATION_TIMEOUT": "10s", "BATON_MAX_AUTO_DECISIONS": "8"}, Escalation{10 * time.Second, 8}, ""},
		{"too short in the file", `{"escalation_timeout":"30s"}`, nil, Escalation{}, `escalation_timeout "30s" is not valid: use a duration from 1m to 2h`},
		{"too long", `{"escalation_timeout":"3h"}`, nil, Escalation{}, "from 1m to 2h"},
		{"not a duration", `{"escalation_timeout":"5"}`, nil, Escalation{}, `escalation_timeout "5"`},
		{"too short even for tests", `{}`, map[string]string{"BATON_ESCALATION_TIMEOUT": "5s"}, Escalation{}, `BATON_ESCALATION_TIMEOUT "5s" is not valid: use a duration from 10s to 2h`},
		{"negative cap", `{"max_auto_decisions":-1}`, nil, Escalation{}, "max_auto_decisions -1 is not valid"},
		{"bad cap in env", `{}`, map[string]string{"BATON_MAX_AUTO_DECISIONS": "many"}, Escalation{}, `BATON_MAX_AUTO_DECISIONS "many" is not valid`},
	} {
		got, err := load(c.file, c.env)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want an error mentioning %q", c.name, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
}

// The host passes the settings down to the session, where the CLI and the hooks read them back.
func TestVerifyTimeout(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		name string
		file string
		env  map[string]string
		want time.Duration
		err  string
	}{
		{"default", `{}`, nil, 3 * time.Hour, ""},
		{"file", `{"verify_timeout":"45m"}`, nil, 45 * time.Minute, ""},
		{"file bounds", `{"verify_timeout":"24h"}`, nil, 24 * time.Hour, ""},
		{"env wins", `{"verify_timeout":"45m"}`, map[string]string{"BATON_VERIFY_TIMEOUT": "2s"}, 2 * time.Second, ""},
		{"too short in the file", `{"verify_timeout":"30s"}`, nil, 0, `verify_timeout "30s" is not valid: use a duration from 1m to 24h`},
		{"too long", `{"verify_timeout":"25h"}`, nil, 0, "from 1m to 24h"},
		{"not a duration", `{"verify_timeout":"3"}`, nil, 0, `verify_timeout "3"`},
		{"too short even for tests", `{}`, map[string]string{"BATON_VERIFY_TIMEOUT": "500ms"}, 0, `BATON_VERIFY_TIMEOUT "500ms" is not valid: use a duration from 1s to 24h`},
	} {
		os.WriteFile(Path(root), []byte(c.file), 0o644)
		got, err := Load(root, func(k string) string { return c.env[k] }).JobTimeout()
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err) || got != DefaultVerifyTimeout):
			t.Errorf("%s: %s %v, want the default and an error mentioning %q", c.name, got, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %s %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestSwitchAfter(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		name string
		file string
		env  map[string]string
		want time.Duration
		err  string
	}{
		{"default", `{}`, nil, 5 * time.Minute, ""},
		{"file", `{"switch_after":"20m"}`, nil, 20 * time.Minute, ""},
		{"env wins", `{"switch_after":"20m"}`, map[string]string{"BATON_SWITCH_AFTER": "5s"}, 5 * time.Second, ""},
		{"too short in the file", `{"switch_after":"10s"}`, nil, 0, `switch_after "10s" is not valid: use a duration from 1m to 2h, such as "5m"`},
		{"too long", `{"switch_after":"3h"}`, nil, 0, "from 1m to 2h"},
		{"too short even for tests", `{}`, map[string]string{"BATON_SWITCH_AFTER": "0s"}, 0, `BATON_SWITCH_AFTER "0s" is not valid: use a duration from 1s to 2h`},
	} {
		os.WriteFile(Path(root), []byte(c.file), 0o644)
		got, err := Load(root, func(k string) string { return c.env[k] }).SwitchWait()
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err) || got != DefaultSwitchAfter):
			t.Errorf("%s: %s %v, want the default and an error mentioning %q", c.name, got, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %s %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestForegroundMax(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		name string
		file string
		env  map[string]string
		want time.Duration
		err  string
	}{
		{"default", `{}`, nil, 2 * time.Minute, ""},
		{"file", `{"foreground_max":"30s"}`, nil, 30 * time.Second, ""},
		{"off", `{"foreground_max":"off"}`, nil, 0, ""},
		{"env wins", `{"foreground_max":"5m"}`, map[string]string{"BATON_FOREGROUND_MAX": "3s"}, 3 * time.Second, ""},
		{"env off wins", `{"foreground_max":"5m"}`, map[string]string{"BATON_FOREGROUND_MAX": "off"}, 0, ""},
		{"env on over a file off", `{"foreground_max":"off"}`, map[string]string{"BATON_FOREGROUND_MAX": "1m"}, time.Minute, ""},
		{"too short in the file", `{"foreground_max":"5s"}`, nil, 0, `foreground_max "5s" is not valid: use a duration from 10s to 10m, such as "2m"`},
		{"longer than Claude Code goes", `{"foreground_max":"15m"}`, nil, 0, "from 10s to 10m"},
		{"not a duration", `{"foreground_max":"never"}`, nil, 0, `foreground_max "never"`},
	} {
		os.WriteFile(Path(root), []byte(c.file), 0o644)
		got, err := Load(root, func(k string) string { return c.env[k] }).ForegroundWait()
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err) || got != DefaultForegroundMax):
			t.Errorf("%s: %s %v, want the default and an error mentioning %q", c.name, got, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %s %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestEscalationEnvRoundTrip(t *testing.T) {
	e := Escalation{Timeout: 20 * time.Second, MaxAuto: 0}
	m := map[string]string{}
	for _, kv := range e.Env() {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if got := EscalationFromEnv(func(k string) string { return m[k] }); got != e {
		t.Fatalf("%+v, want %+v", got, e)
	}
	if got := EscalationFromEnv(func(string) string { return "" }); got != (Escalation{DefaultEscalationTimeout, DefaultMaxAutoDecisions}) {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestWorkerSettings(t *testing.T) {
	root := t.TempDir()
	const valid = `is not valid: use "auto", or a number from 0 to 8`
	for _, c := range []struct {
		name    string
		file    string
		env     map[string]string
		workers int
		auto    bool
		usage   float64
		err     string
	}{
		{"defaults: auto, and no usage check", `{}`, nil, 0, true, 0, ""},
		{"auto in the file", `{"workers":"auto"}`, nil, 0, true, 0, ""},
		{"a number", `{"workers":3,"worker_usage_max":50}`, nil, 3, false, 50, ""},
		{"the most", `{"workers":8}`, nil, 8, false, 0, ""},
		{"a number written as a string", `{"workers":"2"}`, nil, 2, false, 0, ""},
		{"none: one phase at a time", `{"workers":0}`, nil, 0, false, 0, ""},
		{"env wins", `{"workers":3,"worker_usage_max":50}`, map[string]string{"BATON_WORKERS": "0", "BATON_WORKER_USAGE_MAX": "95.5"}, 0, false, 95.5, ""},
		{"auto in the env", `{"workers":3}`, map[string]string{"BATON_WORKERS": "auto"}, 0, true, 0, ""},
		{"too many", `{"workers":9}`, nil, 0, true, 0, "workers 9 " + valid},
		{"fewer than none", `{"workers":-1}`, nil, 0, true, 0, "workers -1 " + valid},
		{"not a whole number", `{"workers":2.5}`, nil, 0, true, 0, "workers 2.5 " + valid},
		{"another word", `{"workers":"lots"}`, nil, 0, true, 0, `workers "lots" ` + valid},
		{"another kind", `{"workers":true}`, nil, 0, true, 0, "workers true " + valid},
		{"not a number in the env", `{}`, map[string]string{"BATON_WORKERS": "two"}, 0, true, 0, `BATON_WORKERS "two" ` + valid},
		{"over 100 percent", `{"worker_usage_max":120}`, nil, 0, true, 0, "worker_usage_max 120 is not valid"},
		{"a negative percent", `{}`, map[string]string{"BATON_WORKER_USAGE_MAX": "-5"}, 0, true, 0, "worker_usage_max -5"},
		{"not a percent", `{}`, map[string]string{"BATON_WORKER_USAGE_MAX": "most"}, 0, true, 0, `BATON_WORKER_USAGE_MAX "most"`},
	} {
		os.WriteFile(Path(root), []byte(c.file), 0o644)
		cfg := Load(root, func(k string) string { return c.env[k] })
		n, auto, nerr := cfg.WorkerCount()
		f, ferr := cfg.WorkerUsage()
		err := errors.Join(nerr, ferr)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want an error mentioning %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		}
		if n != c.workers || auto != c.auto || f != c.usage {
			t.Errorf("%s: workers %d (auto %v), usage max %v; want %d (auto %v), %v", c.name, n, auto, f, c.workers, c.auto, c.usage)
		}
	}
}

// A value of the wrong kind for workers is reported, and the settings after it in the file still count.
func TestABadWorkersKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(Path(root), []byte(`{"workers":{"most":3},"ntfy_topic":"after","worker_usage_max":70}`), 0o644)
	cfg := Load(root, func(string) string { return "" })
	if _, _, err := cfg.WorkerCount(); err == nil || !strings.Contains(err.Error(), `workers {"most":3} is not valid`) {
		t.Errorf("WorkerCount: %v", err)
	}
	if f, err := cfg.WorkerUsage(); cfg.NtfyTopic != "after" || f != 70 || err != nil {
		t.Errorf("the settings after it: topic %q, usage max %v %v", cfg.NtfyTopic, f, err)
	}
}

// With auto, a worker for each 2 CPUs or 4 GiB, whichever allows fewer, less one for the lead's session.
func TestAutoWorkers(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name string
		m    machine.Reading
		want int
	}{
		{"a roomy Mac", machine.Reading{CPUs: 14, MemTotal: 48 * gib}, 6},
		{"an 8 GB, 8-core laptop", machine.Reading{CPUs: 8, MemTotal: 8 * gib}, 1},
		{"Linux leaves the kernel's share out of 8 GB (7.6 GiB)", machine.Reading{CPUs: 8, MemTotal: 76 * gib / 10}, 1},
		{"4 GB", machine.Reading{CPUs: 8, MemTotal: 4 * gib}, 0},
		{"two CPUs", machine.Reading{CPUs: 2, MemTotal: 64 * gib}, 0},
		{"memory not known", machine.Reading{CPUs: 16}, 0},
		{"a big server", machine.Reading{CPUs: 128, MemTotal: 1024 * gib}, 8},
	} {
		if got := AutoWorkers(c.m); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	root := t.TempDir()
	read := func() machine.Reading { return machine.Reading{CPUs: 14, MemTotal: 48 * gib} }
	for file, want := range map[string]int{`{}`: 6, `{"workers":2}`: 2, `{"workers":0}`: 0, `{"workers":"x"}`: 0} {
		os.WriteFile(Path(root), []byte(file), 0o644)
		if n, _, _ := Load(root, func(string) string { return "" }).WorkerCap(read); n != want {
			t.Errorf("%s: %d workers, want %d", file, n, want)
		}
	}
}

// A worker starts in the lead session's mode: the one its flags gave it, which the host passes on, or else
// the default mode of its settings: the project's local ones, its shared ones, then the user's.
func TestWorkerMode(t *testing.T) {
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(body), 0o644)
	}
	for _, c := range []struct {
		name               string
		flag, project, usr string
		local              bool
		want               string
	}{
		{"nothing set", "", "", "", false, ""},
		{"the user's", "", "", "auto", false, "auto"},
		{"the project's first", "", "acceptEdits", "auto", false, "acceptEdits"},
		{"the lead's flags first", "plan", "acceptEdits", "auto", true, "plan"},
		{"the project's local settings first", "", "acceptEdits", "auto", true, "bypassPermissions"},
	} {
		t.Run(c.name, func(t *testing.T) {
			checkout, home := t.TempDir(), t.TempDir()
			if c.project != "" {
				write(filepath.Join(checkout, ".claude", "settings.json"), `{"permissions":{"defaultMode":"`+c.project+`"}}`)
			}
			if c.usr != "" {
				write(filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"defaultMode":"`+c.usr+`","allow":["Bash"]}}`)
			}
			if c.local {
				write(filepath.Join(checkout, ".claude", "settings.local.json"), `{"permissions":{"defaultMode":"bypassPermissions"}}`)
			}
			env := map[string]string{"HOME": home, "BATON_WORKER_MODE": c.flag}
			if mode := WorkerMode(func(k string) string { return env[k] }, checkout); mode != c.want {
				t.Errorf("mode %q, want %q", mode, c.want)
			}
		})
	}
}
