package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.0", "v0.1.0", 0}, {"0.1.0", "0.1.0-rc.2", 1}, {"0.1.0-rc.2", "0.1.0-rc.10", -1},
		{"0.1.0-rc.2", "0.1.0-rc.2", 0}, {"0.2.0", "0.1.9", 1}, {"1.0.0", "0.99.99", 1}, {"0.1.0-rc.1", "0.1.0-beta.1", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.3.1": true, "v0.4.0-rc.1": true, "v10.20.30": true,
		"dev": false, "0.3.1": false, "v0.3.0-16-g35ada53-dirty": false, "v0.3.0-16-g35ada53": false, "v0.3": false, "": false,
	} {
		if got := Release(v); got != want {
			t.Errorf("Release(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompatible(t *testing.T) {
	for _, c := range []struct {
		running, installed string
		want               bool
	}{
		{"v0.3.1", "v0.3.2", true},
		{"v0.3.1", "v0.3.10", true},
		{"v1.2.0", "v1.3.0", true},
		{"v1.2.0", "v1.2.1", true},
		{"v0.3.0-rc.1", "v0.3.0", true}, // out of a pre-release into its release
		{"v0.3.2", "v0.4.0", false},     // before 1.0, the minor version is the major one
		{"v1.3.0", "v2.0.0", false},
		{"v0.0.1", "v0.0.2", false}, // 0.0.x: every release may break
		{"v0.3.2", "v0.3.1", false}, // never backwards
		{"v0.3.2", "v0.3.2", false},
		{"v0.3.1", "v0.3.2-rc.1", false}, // never onto a pre-release
		{"dev", "v0.3.2", false},
		{"v0.3.0-16-g35ada53-dirty", "v0.3.2", false},
		{"v0.3.1", "garbage", false},
	} {
		if got := Compatible(c.running, c.installed); got != c.want {
			t.Errorf("Compatible(%s, %s) = %v, want %v", c.running, c.installed, got, c.want)
		}
	}
}

// install puts a baton binary for each version under root and links the last as the installed one,
// the way the plugin's launcher does.
func install(t *testing.T, root string, versions ...string) {
	t.Helper()
	for _, v := range versions {
		p := Bin(root, v)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho baton "+v+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "bin", "baton")
	os.Remove(link)
	if err := os.Symlink(filepath.Join(versions[len(versions)-1], filepath.Base(Bin(root, "x"))), link); err != nil {
		t.Fatal(err)
	}
}

func TestInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher links versions on macOS and Linux only")
	}
	root := t.TempDir()
	if _, ok := Installed(root); ok {
		t.Fatal("nothing is installed yet")
	}
	install(t, root, "v0.3.1", "v0.3.2")
	if v, ok := Installed(root); !ok || v != "v0.3.2" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if v, err := Probe(Bin(root, "v0.3.2")); err != nil || v != "v0.3.2" {
		t.Fatalf("Probe = %q, %v", v, err)
	}
	os.Remove(Bin(root, "v0.3.2")) // linked, but its binary is gone
	if _, ok := Installed(root); ok {
		t.Fatal("a link to a missing binary is not an installed version")
	}
}

func TestWatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher links versions on macOS and Linux only")
	}
	root, now := t.TempDir(), time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	install(t, root, "v0.3.1")
	w := &Watch{Root: root, Running: "v0.3.1", Every: 15 * time.Second, Now: func() time.Time { return now }}
	if v := w.Newer(); v != "" {
		t.Fatalf("nothing newer yet: %q", v)
	}
	install(t, root, "v0.3.2")
	if v := w.Newer(); v != "" {
		t.Fatalf("looked again before Every: %q", v)
	}
	now = now.Add(15 * time.Second)
	if v := w.Newer(); v != "v0.3.2" {
		t.Fatalf("Newer = %q", v)
	}
	install(t, root, "v0.4.0")
	now = now.Add(15 * time.Second)
	if v := w.Newer(); v != "" {
		t.Fatalf("a major update is not taken: %q", v)
	}
	install(t, root, "v0.3.3")
	w.Skip = "v0.3.3"
	now = now.Add(15 * time.Second)
	if v := w.Newer(); v != "" {
		t.Fatalf("a version a restart failed on is skipped: %q", v)
	}
}
