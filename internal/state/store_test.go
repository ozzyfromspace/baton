package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestUpdateWritesOnlyAChange(t *testing.T) {
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC) // set on state.json before each update: a write moves it
	cases := []struct {
		name   string
		before func(st *State) // the state on disk, nil: no state.json yet
		fn     func(st *State)
		writes bool
	}{
		{"nothing changed", func(st *State) { st.Mode = ModeRunning }, func(st *State) {}, false},
		{"a field set to what it was", func(st *State) { st.Mode = ModeRunning }, func(st *State) { st.Mode = ModeRunning }, false},
		{"a phase map set again", func(st *State) { st.Phases["P1"] = &PhaseState{Status: PhaseWorking} },
			func(st *State) { st.Phases["P1"] = &PhaseState{Status: PhaseWorking} }, false},
		{"a field changed", func(st *State) { st.Mode = ModeRunning }, func(st *State) { st.Mode = ModePaused }, true},
		{"a phase added", func(st *State) {}, func(st *State) { st.Phases["P1"] = &PhaseState{Status: PhasePending} }, true},
		{"no state yet, nothing changed", nil, func(st *State) {}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Open(t.TempDir(), "i", func() time.Time { return t0 })
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Dir, "state.json")
			var before os.FileInfo
			if c.before != nil {
				if _, err := s.Update(func(st *State) error { c.before(st); return nil }); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
				if before, err = os.Stat(path); err != nil {
					t.Fatal(err)
				}
			}
			want := New()
			if c.before != nil {
				c.before(&want)
			}
			c.fn(&want)
			got, err := s.Update(func(st *State) error { c.fn(st); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != want.Mode || len(got.Phases) != len(want.Phases) {
				t.Fatalf("Update returned mode %q, %d phases; want %q, %d", got.Mode, len(got.Phases), want.Mode, len(want.Phases))
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if before == nil {
				return // a state.json was written where there was none
			}
			moved := !after.ModTime().Equal(old)
			if moved != c.writes {
				t.Errorf("state.json written: %v (modified %s), want %v", moved, after.ModTime(), c.writes)
			}
			// On Windows, SameFile looks both files up by path when it is called, so it can't tell a
			// replaced file from the one it replaced.
			if runtime.GOOS != "windows" && os.SameFile(before, after) == c.writes {
				t.Errorf("state.json replaced: %v, want %v", !os.SameFile(before, after), c.writes)
			}
			if st, err := s.Load(); err != nil || st.Mode != want.Mode || len(st.Phases) != len(want.Phases) {
				t.Errorf("on disk: mode %q, %d phases (err %v); want %q, %d", st.Mode, len(st.Phases), err, want.Mode, len(want.Phases))
			}
		})
	}
}
