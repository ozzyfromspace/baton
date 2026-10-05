package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadFileEnvAndDefaults(t *testing.T) {
	root := t.TempDir()
	c := Load(root, func(string) string { return "" })
	if c.Autocompact != DefaultAutocompact || *c.CheckpointPct != DefaultCheckpointPct || *c.WarnPct != DefaultWarnPct || c.NtfyServer != DefaultNtfyServer || !c.DesktopOn() {
		t.Fatalf("defaults: %+v", c)
	}
	os.WriteFile(Path(root), []byte(`{"ntfy_topic":"file","details":true,"autocompact":"off","checkpoint_pct":50,"warn_pct":0}`), 0o644)
	c = Load(root, func(string) string { return "" })
	if c.NtfyTopic != "file" || !c.Details || c.Autocompact != "" || *c.CheckpointPct != 50 || *c.WarnPct != 0 {
		t.Fatalf("file: %+v", c)
	}
	env := map[string]string{"BATON_NTFY_TOPIC": "env", "BATON_NOTIFY_DESKTOP": "0", "BATON_AUTOCOMPACT": "600k", "BATON_CHECKPOINT_PCT": "0", "BATON_WARN_PCT": "80"}
	c = Load(root, func(k string) string { return env[k] })
	if c.NtfyTopic != "env" || c.DesktopOn() || c.Autocompact != "600k" || *c.CheckpointPct != 0 || *c.WarnPct != 80 {
		t.Fatalf("env: %+v", c)
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
