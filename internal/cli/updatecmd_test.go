//go:build !windows

package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdatedSaysWhatBecomesOfRunningSessions(t *testing.T) {
	for _, c := range []struct {
		running, to string
		restarts    bool
		want        string
	}{
		{"v0.3.1", "v0.3.2", true, "Sessions baton hosts restart on it by themselves once idle"},
		{"v0.3.1", "v0.3.2", false, "Sessions already running keep the old version until they restart"},
		{"v0.3.2", "v0.4.0", true, "a major update. Sessions already running stay on v0.3.2 until they restart"},
		{"dev", "v0.4.0", true, "Sessions baton hosts restart on it by themselves"},
	} {
		if got := updated(c.running, c.to, c.restarts); !strings.Contains(got, c.want) {
			t.Errorf("%s → %s (restarts %v): %q", c.running, c.to, c.restarts, got)
		}
	}
}

// fakeClaude is a `claude` that reports the baton plugin at the version in its state file and, on
// `plugin update`, moves it to the next one, with a launcher that "downloads" the matching binary.
func fakeClaude(t *testing.T, dir, from, to string) (claude string, calls func() string) {
	t.Helper()
	install := func(v string) string {
		p := filepath.Join(dir, "cache", v)
		os.MkdirAll(filepath.Join(p, "bin"), 0o755)
		os.WriteFile(filepath.Join(p, "bin", "baton"), []byte("#!/bin/sh\necho \"baton v"+v+"\" BATON_BIN=${BATON_BIN:-unset}\n"), 0o755)
		return p
	}
	install(from)
	install(to)
	state, log := filepath.Join(dir, "version"), filepath.Join(dir, "calls")
	os.WriteFile(state, []byte(from), 0o644)
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
v=$(cat %[2]q)
case "$*" in
  "plugin list --json") printf '[{"id":"other@x","version":"9"},{"id":"baton@baton","version":"%%s","scope":"user","installPath":"%[3]s/%%s"}]' "$v" "$v" ;;
  "plugin update baton@baton --scope user") printf %%s %[4]q > %[2]q ;;
esac
`, log, state, filepath.Join(dir, "cache"), to)
	claude = filepath.Join(dir, "claude")
	os.WriteFile(claude, []byte(script), 0o755)
	return claude, func() string { b, _ := os.ReadFile(log); return string(b) }
}

func TestUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name": "v0.2.0"}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	claude, calls := fakeClaude(t, dir, "0.1.0", "0.2.0")
	env := map[string]string{"BATON_CLAUDE": claude, "BATON_RELEASES_URL": srv.URL}

	io, out, _ := testIO("", env)
	if code := Main([]string{"update", "--check"}, io); code != 0 || !strings.Contains(out.String(), "v0.2.0 is available") || strings.Contains(calls(), "plugin update") {
		t.Fatalf("--check: code %d out %q calls %q", code, out.String(), calls())
	}

	t.Setenv("BATON_BIN", "/the/running/binary")
	io, out, errb := testIO("", env)
	if code := Main([]string{"update"}, io); code != 0 {
		t.Fatalf("update: code %d out %q err %q", code, out.String(), errb.String())
	}
	if c := calls(); !strings.Contains(c, "plugin marketplace update baton\n") || !strings.Contains(c, "plugin update baton@baton --scope user\n") {
		t.Fatalf("calls %q", c)
	}
	// The new plugin's launcher ran (fetching its binary), without being pointed back at the old one.
	if o := out.String(); !strings.Contains(o, "baton v0.2.0 BATON_BIN=unset") || !strings.Contains(o, "updated to v0.2.0") {
		t.Fatalf("out %q", o)
	}

	io, out, _ = testIO("", env)
	if code := Main([]string{"update"}, io); code != 0 || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("second update: code %d out %q", code, out.String())
	}
}
