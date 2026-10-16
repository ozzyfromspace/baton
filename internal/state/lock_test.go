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

// holdTimers takes afterHold over for a test: a holding records itself only when the test fires its timer.
type holdTimers struct {
	t       *testing.T
	records []func()
	stopped int
}

func takeOverHold(t *testing.T) *holdTimers {
	h := &holdTimers{t: t}
	old := afterHold
	afterHold = func(d time.Duration, record func()) func() bool {
		if d != holderAfter {
			t.Errorf("a holding records itself after %s, want %s", d, holderAfter)
		}
		h.records = append(h.records, record)
		return func() bool { h.stopped++; return true }
	}
	t.Cleanup(func() { afterHold = old })
	return h
}

// fire runs every holding's timer, as if each had held its lock for holderAfter.
func (h *holdTimers) fire() {
	for _, f := range h.records {
		f()
	}
}

// recordAtOnce makes every holding record itself as it takes its lock.
func recordAtOnce(t *testing.T) {
	old := afterHold
	afterHold = func(_ time.Duration, record func()) func() bool { record(); return func() bool { return false } }
	t.Cleanup(func() { afterHold = old })
}

func TestLockHolderRecordedWhileHeld(t *testing.T) {
	timers := takeOverHold(t)
	s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	var during lockHolder
	var early, known bool
	if _, err := s.Update(func(st *State) error {
		_, early = readHolder(s.path("lock"))
		timers.fire()
		during, known = readHolder(s.path("lock"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if early {
		t.Fatal("recorded before the lock was held for holderAfter")
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

// A holder that keeps its lock records itself on its own, by the real timer, and clears its record as it
// lets the lock go.
func TestALongHoldRecordsItself(t *testing.T) {
	s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	var h lockHolder
	var known bool
	s.Update(func(st *State) error {
		for end := time.Now().Add(10 * time.Second); !known && time.Now().Before(end); {
			time.Sleep(10 * time.Millisecond)
			h, known = readHolder(s.path("lock"))
		}
		return nil
	})
	if !known || h.PID != os.Getpid() || !strings.Contains(h.Caller, "TestALongHoldRecordsItself") {
		t.Fatalf("while held: %+v (known %v)", h, known)
	}
	if h, known := readHolder(s.path("lock")); known {
		t.Fatalf("after: %+v still recorded", h)
	}
}

// A hold shorter than holderAfter, a read of the state or an update, leaves the lock file as it was, and a
// holding's timer that fires once its lock is let go records nothing.
func TestAShortHoldWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold func(s *Store) error
	}{
		{"a read", func(s *Store) error { _, err := s.Load(); return err }},
		{"an update", func(s *Store) error {
			_, err := s.Update(func(st *State) error { st.Mode = ModeRunning; return nil })
			return err
		}},
		{"a failed update", func(s *Store) error {
			s.Update(func(st *State) error { return os.ErrInvalid })
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timers := takeOverHold(t)
			s, err := Open(t.TempDir(), "test", func() time.Time { return t0 })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(); err != nil { // the lock file is made as the lock is first taken
				t.Fatal(err)
			}
			lock := s.path("lock")
			old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			if err := os.Chtimes(lock, old, old); err != nil {
				t.Fatal(err)
			}
			if err := tc.hold(s); err != nil {
				t.Fatal(err)
			}
			timers.fire() // too late: every lock was let go
			fi, err := os.Stat(lock)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Size() != 0 || !fi.ModTime().Equal(old) {
				t.Errorf("lock file written: %d bytes, modified %s", fi.Size(), fi.ModTime())
			}
			if timers.stopped != len(timers.records) {
				t.Errorf("%d holdings, %d timers stopped", len(timers.records), timers.stopped)
			}
		})
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

// A panic under the lock, in the work or in the clock or the log the lock keeps, lets the lock go and leaves
// no holder recorded: a hook that fails open, or a host that recovers, takes the lock again at once.
func TestLockLetGoOnPanic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		panicAt int  // the clock's call that panics (1: as the lock is taken, 2: as it is let go), 0 for none
		work    bool // the work panics
		log     bool // the log panics (on a slow hold)
	}{
		{"the clock as the lock is taken", 1, false, false},
		{"the work", 0, true, false},
		{"the clock as the lock is let go", 2, false, false},
		{"the log of a slow hold", 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordAtOnce(t) // so that a recorded holder is there to be cleared
			path := filepath.Join(t.TempDir(), "lock")
			calls := 0
			w := LockWatch{
				Now: func() time.Time {
					if calls++; calls == tc.panicAt {
						panic("boom")
					}
					return t0.Add(time.Duration(calls) * time.Second)
				},
				Event: func(string, map[string]any) {
					if tc.log {
						panic("boom")
					}
				},
			}
			func() {
				defer func() { recover() }()
				withLockFile(path, w, func() error {
					if tc.work {
						panic("boom")
					}
					return nil
				})
			}()
			other := flock.New(path)
			if ok, err := other.TryLock(); !ok || err != nil {
				t.Fatalf("the lock was kept: %v %v", ok, err)
			}
			other.Unlock()
			if h, known := readHolder(path); known {
				t.Errorf("still recorded: %+v", h)
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
