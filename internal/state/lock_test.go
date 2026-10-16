package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// lockEvents are the events of kind in s's events.jsonl.
func lockEvents(t *testing.T, s *Store, kind string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestLockHolderRecordedWhileHeld(t *testing.T) {
	s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	var during lockHolder
	var known bool
	if _, err := s.Update(func(st *State) error {
		during, known = readHolder(s.path("lock"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !known || during.PID != os.Getpid() || !during.Since.Equal(t0) ||
		!strings.Contains(during.Caller, "TestLockHolderRecordedWhileHeld") || !strings.Contains(during.Caller, "lock_test.go:") {
		t.Fatalf("while held: %+v (known %v)", during, known)
	}
	if h, known := readHolder(s.path("lock")); known {
		t.Fatalf("after: %+v still recorded", h)
	}
	// A longer record before a shorter one leaves nothing of itself in what is read.
	writeHolder(s.path("lock"), lockHolder{PID: 7, Caller: strings.Repeat("x", 300), Since: t0})
	clearHolder(s.path("lock"))
	writeHolder(s.path("lock"), lockHolder{PID: 8, Caller: "short", Since: t0})
	if h, _ := readHolder(s.path("lock")); h.PID != 8 || h.Caller != "short" {
		t.Fatalf("after a longer record: %+v", h)
	}
}

func TestLockSlowHoldLogged(t *testing.T) {
	for _, tc := range []struct {
		held time.Duration
		want string // held_for logged, "" for no event
	}{
		{0, ""},
		{999 * time.Millisecond, ""},
		{time.Second, "1s"},
		{3500 * time.Millisecond, "3.5s"},
	} {
		t.Run(tc.held.String(), func(t *testing.T) {
			now := t0
			s, err := Open(t.TempDir(), "test", func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Update(func(st *State) error { now = now.Add(tc.held); return nil }); err != nil {
				t.Fatal(err)
			}
			got := lockEvents(t, s, "lock_slow")
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("logged %v", got)
				}
				return
			}
			if len(got) != 1 || got[0]["held_for"] != tc.want || got[0]["lock"] != filepath.Base(s.Dir)+"/lock" ||
				!strings.Contains(got[0]["caller"].(string), "TestLockSlowHoldLogged") {
				t.Fatalf("logged %v", got)
			}
		})
	}
}

func TestLockTimeoutNamesHolder(t *testing.T) {
	defer func(d time.Duration) { lockTimeout = d }(lockTimeout)
	lockTimeout = 60 * time.Millisecond
	for _, tc := range []struct {
		name     string
		holder   *lockHolder
		wantErr  []string
		wantSeen map[string]any
	}{
		{"recorded", &lockHolder{PID: 4242, Caller: "loop.(*Coordinator).land (merge.go:490)", Since: t0.Add(-7 * time.Second)},
			[]string{"locked by another process", "pid 4242", "merge.go:490", "7s"},
			map[string]any{"holder": float64(4242), "caller": "loop.(*Coordinator).land (merge.go:490)", "held_for": "7s"}},
		{"nothing recorded", nil, []string{"locked by another process"}, map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
			if err != nil {
				t.Fatal(err)
			}
			other := flock.New(s.path("lock")) // another process's hold
			if ok, err := other.TryLock(); !ok || err != nil {
				t.Fatalf("hold: %v %v", ok, err)
			}
			defer other.Unlock()
			if tc.holder != nil {
				writeHolder(s.path("lock"), *tc.holder)
			}
			ran := false
			_, err = s.Update(func(st *State) error { ran = true; return nil })
			if err == nil || ran {
				t.Fatalf("err %v, ran %v", err, ran)
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			got := lockEvents(t, s, "lock_timeout")
			if len(got) != 1 {
				t.Fatalf("events %v", got)
			}
			e := got[0]
			if !strings.Contains(e["waiter"].(string), "TestLockTimeoutNamesHolder") {
				t.Errorf("waiter %v", e["waiter"])
			}
			for _, k := range []string{"holder", "caller", "held_for"} {
				if e[k] != tc.wantSeen[k] {
					t.Errorf("%s: %v, want %v", k, e[k], tc.wantSeen[k])
				}
			}
		})
	}
}

func TestProjectLockLogsToBatonLog(t *testing.T) {
	now := t0
	p, err := OpenProject(filepath.Join(t.TempDir(), DirName), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.withLock(func() error { now = now.Add(2 * time.Second); return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.Dir, "baton.log"))
	if line := string(b); !strings.Contains(line, " lock_slow ") || !strings.Contains(line, `"held_for":"2s"`) ||
		!strings.Contains(line, "TestProjectLockLogsToBatonLog") {
		t.Fatalf("baton.log: %q", line)
	}
}

func TestLockPlumbing(t *testing.T) {
	for name, want := range map[string]bool{
		"state.withLockFile":                true,
		"state.(*Store).Update":             true,
		"state.(*Store).Load":               true,
		"state.(*Project).withLock":         true,
		"state.Locked":                      true,
		"hooks.(*handlers).update":          true,
		"loop.(*Loop).update":               true,
		"bridge.(*Bridge).locked":           true,
		"state.(*Store).Relay":              false,
		"hooks.(*handlers).toolDone":        false,
		"loop.(*Coordinator).land.func1":    false,
		"hooks.(*handlers).updateSomething": false,
	} {
		if got := lockPlumbing(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
