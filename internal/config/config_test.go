package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileEnvAndDefaults(t *testing.T) {
	home := t.TempDir()
	c := Load(home, func(string) string { return "" })
	if c.Autocompact != DefaultAutocompact || *c.CheckpointPct != DefaultCheckpointPct || c.NtfyServer != DefaultNtfyServer || !c.DesktopOn() {
		t.Fatalf("defaults: %+v", c)
	}
	os.MkdirAll(filepath.Join(home, ".baton"), 0o755)
	os.WriteFile(Path(home), []byte(`{"ntfy_topic":"file","details":true,"autocompact":"off","checkpoint_pct":50}`), 0o644)
	c = Load(home, func(string) string { return "" })
	if c.NtfyTopic != "file" || !c.Details || c.Autocompact != "" || *c.CheckpointPct != 50 {
		t.Fatalf("file: %+v", c)
	}
	env := map[string]string{"BATON_NTFY_TOPIC": "env", "BATON_NOTIFY_DESKTOP": "0", "BATON_AUTOCOMPACT": "600k", "BATON_CHECKPOINT_PCT": "0"}
	c = Load(home, func(k string) string { return env[k] })
	if c.NtfyTopic != "env" || c.DesktopOn() || c.Autocompact != "600k" || *c.CheckpointPct != 0 {
		t.Fatalf("env: %+v", c)
	}
}
