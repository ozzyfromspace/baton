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

	"github.com/ozzyfromspace/baton/internal/version"
)

func TestUpdatedSaysWhatBecomesOfRunningSessions(t *testing.T) {
	for _, c := range []struct {
		running, to         string
		restarts, shellHook bool
		want                []string
	}{
		{"v0.3.2", "v0.3.3", true, true, []string{"baton: updated to v0.3.3. Sessions baton hosts restart on it by themselves once idle", "Plain claude sessions use it the next time they run /baton."}},
		{"v0.3.2", "v0.3.3", false, true, []string{"Sessions baton hosts stay on their version until you restart them", "the next time they run /baton"}},
		{"v0.3.2", "v0.4.0", true, true, []string{"This is a major update: sessions baton hosts stay on v0.3.2 until you restart them", "Read what changed first"}},
		{"v0.3.2", "v0.3.3", true, false, []string{"Plain claude sessions use it once they restart."}},
		{"dev", "v0.4.0", true, true, []string{"Sessions baton hosts restart on it by themselves"}},
	} {
		got := updated(c.running, c.to, c.restarts, c.shellHook)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%+v: no %q in %q", c, want, got)
			}
		}
	}
}

// Inside a session that has not restarted on the update yet, `baton update` runs on the session's own,
// older binary. The installed one is current, so there is nothing to download, and it says when the
// session moves over instead of claiming to have updated again.
func TestUpdateInASessionNotYetRestarted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name": "v0.3.3"}`)
	}))
	defer srv.Close()
	old := version.Version
	version.Version = "v0.3.2"
	defer func() { version.Version = old }()
	dir, home := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, "bin", "v0.3.3"), 0o755)
	os.WriteFile(filepath.Join(home, "bin", "v0.3.3", "baton"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink(filepath.Join("v0.3.3", "baton"), filepath.Join(home, "bin", "baton"))
	claude, calls := fakeClaude(t, dir, "0.3.3", "0.3.3")
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"BATON_HOST": "1"}, "baton: up to date: v0.3.3 is installed. This session runs v0.3.2 until it restarts on v0.3.3 by itself, once idle"},
		{map[string]string{"BATON_HOST": "1", "BATON_AUTO_RESTART": "0"}, "This session runs v0.3.2 until you restart it: exit and run `baton --continue`."},
		{map[string]string{}, "baton: up to date: v0.3.3 is installed (this copy of baton is v0.3.2)."},
	} {
		c.env["BATON_CLAUDE"], c.env["BATON_RELEASES_URL"], c.env["BATON_HOME"] = claude, srv.URL, home
		io, out, _ := testIO("", c.env)
		if code := Main([]string{"update"}, io); code != 0 || !strings.Contains(out.String(), c.want) || strings.Contains(out.String(), "updated to") {
			t.Errorf("%v: code %d\n%s", c.env, code, out.String())
		}
		if !strings.Contains(out.String(), "installed plugin v0.3.3, binary v0.3.3; this binary v0.3.2") {
			t.Errorf("first line:\n%s", out.String())
		}
	}
	if strings.Contains(calls(), "plugin update") {
		t.Errorf("updated a current plugin: %s", calls())
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
	env := map[string]string{"BATON_CLAUDE": claude, "BATON_RELEASES_URL": srv.URL, "BATON_HOME": t.TempDir()}

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
