package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	if err := syncFile(f); err != nil {
		t.Errorf("syncFile of an open file: %v", err)
	}
	f.Close()
	if err := syncFile(f); err == nil {
		t.Error("syncFile of a closed file: no error")
	}
}

// writeAtomic, which syncs through syncFile, leaves the whole new content in place.
func TestWriteAtomicReplacesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, data := range []string{"first\n", "second, longer\n", "3\n"} {
		if err := writeAtomic(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != data {
			t.Fatalf("read %q (err %v), want %q", b, err, data)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".state.json.tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
