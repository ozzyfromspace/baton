package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFileAndEnvOverrides(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".baton"), 0o755)
	os.WriteFile(filepath.Join(home, ".baton", "config.json"), []byte(`{"ntfy_topic":"file-topic","details":true}`), 0o644)
	c := LoadConfig(home, func(k string) string { return map[string]string{"BATON_NOTIFY_DESKTOP": "0"}[k] })
	if c.NtfyTopic != "file-topic" || !c.Details || c.Desktop == nil || *c.Desktop || c.NtfyServer != "https://ntfy.sh" {
		t.Fatalf("%+v", c)
	}
	c = LoadConfig(home, func(k string) string { return map[string]string{"BATON_NTFY_TOPIC": "env-topic"}[k] })
	if c.NtfyTopic != "env-topic" {
		t.Fatalf("env override: %+v", c)
	}
}

func TestPushIsContentFreeByDefault(t *testing.T) {
	var gotTitle, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotTitle, gotBody, gotPath = r.Header.Get("Title"), string(b), r.URL.Path
	}))
	defer srv.Close()
	off := false
	n := New(Config{NtfyTopic: "t0p1c", NtfyServer: srv.URL, Desktop: &off})
	if err := n.Notify("my-project", "blocked", "the secret migration plan failed"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/t0p1c" || gotTitle != "baton — my-project" || gotBody != "baton needs you: blocked" {
		t.Fatalf("path %q title %q body %q", gotPath, gotTitle, gotBody)
	}
	n = New(Config{NtfyTopic: "t0p1c", NtfyServer: srv.URL, Desktop: &off, Details: true})
	n.Notify("p", "blocked", "need a decision")
	if gotBody != "baton needs you: blocked: need a decision" {
		t.Fatalf("details body %q", gotBody)
	}
}
