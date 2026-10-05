package config

import (
	"os"
	"testing"
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
