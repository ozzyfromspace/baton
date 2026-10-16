package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The record an Update takes under its lock is the plan it replaces, whatever changed before it is saved
// after the lock is let go: the replacing plan, written in the same Update, is not what is kept.
func TestRecordTakenUnderTheLockSavedAfter(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), DirName), "i", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	old := threePhases()
	old.Title = "The widget"
	if err := s.SavePlan(old); err != nil {
		t.Fatal(err)
	}
	s.Update(func(st *State) error { *st = underway(t); return nil })
	var rec *Record
	if _, err := s.Update(func(st *State) error {
		var err error
		if rec, err = s.Record(*st); err != nil {
			return err
		}
		next := threePhases()
		next.Title = "The gadget"
		st.Generation++
		return s.WritePlan(next)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "gen-1")); err == nil {
		t.Fatal("written under the lock")
	}
	dir, err := rec.Save()
	if err != nil || filepath.Base(dir) != "gen-1" || rec.Generation != 1 {
		t.Fatalf("saved %q %v (generation %d)", dir, err, rec.Generation)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "plan.json"))
	if !strings.Contains(string(b), "The widget") {
		t.Errorf("kept plan: %s", b)
	}
	var kept State
	b, _ = os.ReadFile(filepath.Join(dir, "state.json"))
	if json.Unmarshal(b, &kept) != nil || kept.Generation != 1 {
		t.Errorf("kept state: %s", b)
	}
	if dir, err := (*Record)(nil).Save(); dir != "" || err != nil {
		t.Errorf("a nil record: %q %v", dir, err)
	}
}

// Records of one generation saved at once each get a folder of their own.
func TestRecordsSavedAtOnceKeepApart(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), DirName), "i", func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(threePhases()); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Record(underway(t))
	if err != nil || rec == nil {
		t.Fatalf("record: %v %v", rec, err)
	}
	const n = 8
	dirs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dirs[i], _ = rec.Save()
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			t.Fatalf("folders %v", dirs)
		}
		seen[d] = true
	}
	if got := s.Archives(); len(got) != n {
		t.Errorf("archives: %d", len(got))
	}
}
