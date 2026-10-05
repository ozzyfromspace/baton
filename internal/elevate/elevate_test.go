package elevate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKeyNormalizesTerminals(t *testing.T) {
	for in, want := range map[string]string{"/dev/ttys003": "ttys003", "ttys003": "ttys003", "/dev/pts/3": "pts-3", "pts/3": "pts-3"} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecordIsUsedOnceAndOnlyWhenFresh(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	r := Record{SessionID: "s1", TTY: "pts/3", Dir: "/work", Created: now}
	Save(home, r)
	if _, ok := FindSession(home, "s1"); !ok {
		t.Fatal("FindSession")
	}
	if _, err := Take(home, "/dev/pts/3", now); err != ErrNothingToRelaunch {
		t.Fatalf("a record whose claude was not stopped yet must wait: %v", err)
	}
	r.Stopped = now
	Save(home, r)
	got, err := Take(home, "/dev/pts/3", now.Add(5*time.Second))
	if err != nil || got.SessionID != "s1" {
		t.Fatalf("take: %+v %v", got, err)
	}
	if _, err := Take(home, "/dev/pts/3", now.Add(6*time.Second)); err != ErrNothingToRelaunch {
		t.Fatalf("a record must be used once: %v", err)
	}
	Save(home, r)
	if _, err := Take(home, "pts/3", now.Add(RelaunchWindow+time.Second)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale: %v", err)
	}
	if _, err := os.Stat(Path(home, "pts/3")); !os.IsNotExist(err) {
		t.Fatal("a stale record was not removed")
	}
}

func TestKeepArgs(t *testing.T) {
	argv := []string{"claude", "--model", "haiku", "--resume", "abc", "--permission-mode=auto", "--plugin-dir", "/p", "--dangerously-skip-permissions", "write a poem"}
	want := []string{"--model", "haiku", "--permission-mode=auto", "--plugin-dir", "/p", "--dangerously-skip-permissions"}
	if got := KeepArgs(argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestInitScripts(t *testing.T) {
	for _, sh := range []string{"zsh", "bash"} {
		s, err := Init(sh, "/opt/my baton/baton")
		if err != nil || !strings.Contains(s, `'/opt/my baton/baton' relaunch --tty`) || !strings.Contains(s, `${BATON_HOME:-$HOME/.baton}/bin`) {
			t.Errorf("%s: %v\n%s", sh, err, s)
		}
	}
	if _, err := Init("fish", "/b"); err == nil {
		t.Error("fish is not supported yet")
	}
}

func TestShellHookInstalled(t *testing.T) {
	home := t.TempDir()
	none := func(string) string { return "" }
	if ShellHookInstalled(home, none) {
		t.Fatal("empty home")
	}
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`eval "$(baton init zsh)"`+"\n"), 0o644)
	if !ShellHookInstalled(home, none) || !ShellHookInstalled(t.TempDir(), func(k string) string { return map[string]string{"BATON_SHELL_HOOK": "1"}[k] }) {
		t.Fatal("not found")
	}
}
