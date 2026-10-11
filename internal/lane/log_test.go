package lane

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// output is n numbered lines of 11 bytes each: "line 000000\n".
func output(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		fmt.Fprintf(&b, "line %06d\n", i)
	}
	return b.Bytes()
}

var cutLine = regexp.MustCompile(`\n\[baton\] … (\d+) bytes of output cut here …\n`)

func TestTheLogIsCapped(t *testing.T) {
	for _, c := range []struct {
		name      string
		head, max int64
		lines     int
		chunk     int
	}{
		{"under the cap", 100, 1000, 50, 64},
		{"exactly at the cap", 110, 1100, 100, 64},
		{"one cut", 100, 1000, 120, 64},
		{"many cuts, byte by byte", 100, 1000, 2000, 1},
		{"many cuts, in big writes", 100, 1000, 5000, 4096},
		{"a write bigger than the cap", 100, 1000, 500, 1 << 20},
		{"the real sizes", HeadBytes, MaxBytes, 3 << 20, 32 << 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "job.log")
			l, err := createLog(path, c.head, c.max)
			if err != nil {
				t.Fatal(err)
			}
			out := output(c.lines)
			for rest := out; len(rest) > 0; {
				n := min(c.chunk, len(rest))
				if w, err := l.Write(rest[:n]); err != nil || w != n {
					t.Fatalf("Write: %d, %v", w, err)
				}
				rest = rest[n:]
			}
			l.Close()
			got, _ := os.ReadFile(path)
			if int64(len(got)) > c.max {
				t.Errorf("the log is %d bytes, over its cap of %d", len(got), c.max)
			}
			if int64(len(out)) <= c.max {
				if !bytes.Equal(got, out) {
					t.Errorf("a log under its cap was changed")
				}
				return
			}
			m := cutLine.FindSubmatchIndex(got)
			if m == nil || int64(m[0]) != c.head {
				t.Fatalf("no cut line right after the first %d bytes:\n%.300s", c.head, got)
			}
			cut, _ := strconv.Atoi(string(got[m[2]:m[3]]))
			head, tail := got[:m[0]], got[m[1]:]
			if !bytes.Equal(head, out[:c.head]) {
				t.Error("the head is not the start of the output")
			}
			if !bytes.HasSuffix(out, tail) || !bytes.HasPrefix(tail, []byte("line ")) {
				t.Errorf("the tail is not the end of the output, from the start of a line: %.40q", tail)
			}
			if want := len(out) - len(head) - len(tail); cut != want {
				t.Errorf("the cut line says %d bytes, but %d were cut", cut, want)
			}
			if len(cutLine.FindAllIndex(got, -1)) != 1 {
				t.Error("more than one cut line")
			}
		})
	}
}

func TestTail(t *testing.T) {
	for _, c := range []struct {
		name     string
		content  string
		lines    int
		maxBytes int
		want     string
	}{
		{"empty", "", 80, 6 << 10, ""},
		{"fewer lines than asked", "a\nb\n", 80, 6 << 10, "a\nb"},
		{"the last lines", "a\nb\nc\nd\n", 2, 6 << 10, "c\nd"},
		{"no final newline", "a\nb\nc", 2, 6 << 10, "b\nc"},
		{"a line cut by the byte limit is left out", "first line\nsecond\nthird\n", 80, 14, "second\nthird"},
		{"unless it is the only one", "one very long line\n", 80, 8, "ng line"},
		{"both limits", "aaaa\nbbbb\ncccc\ndddd\n", 3, 12, "cccc\ndddd"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "job.log")
			os.WriteFile(path, []byte(c.content), 0o644)
			got, err := Tail(path, c.lines, c.maxBytes)
			if err != nil || got != c.want {
				t.Errorf("Tail = %q, %v; want %q", got, err, c.want)
			}
			if len(got) > c.maxBytes {
				t.Errorf("Tail returned %d bytes, over %d", len(got), c.maxBytes)
			}
		})
	}
	if _, err := Tail(filepath.Join(t.TempDir(), "missing.log"), 80, 6<<10); err == nil {
		t.Error("Tail of a missing log: no error")
	}
}

func TestTailOfACappedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	l, _ := createLog(path, 100, 1000)
	l.Write(output(500))
	l.Close()
	got, _ := Tail(path, 3, 6<<10)
	if want := "line 000497\nline 000498\nline 000499"; got != want {
		t.Errorf("Tail = %q, want %q", got, want)
	}
}
