package procs

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
	"testing/fstest"
)

// A job's tree is its process group and everything descended from it, a group of its own included; not
// the processes beside it, nor its parent.
func TestTree(t *testing.T) {
	all := []row{
		{pid: 1, ppid: 0, pgid: 1},
		{pid: 50, ppid: 1, pgid: 50},    // the runner
		{pid: 60, ppid: 50, pgid: 60},   // the job's command, a group of its own
		{pid: 61, ppid: 60, pgid: 60},   // its child
		{pid: 70, ppid: 61, pgid: 70},   // a grandchild that started a group of its own
		{pid: 71, ppid: 70, pgid: 70},   // and its child
		{pid: 80, ppid: 1, pgid: 60},    // left in the group by a parent that ended
		{pid: 90, ppid: 1, pgid: 90},    // another job's
		{pid: 91, ppid: 90, pgid: 90},   // and its child
		{pid: 100, ppid: 50, pgid: 100}, // the runner's other child
	}
	pids := func(rs []row) []int {
		var out []int
		for _, r := range rs {
			out = append(out, r.pid)
		}
		return out
	}
	for _, c := range []struct {
		name string
		pgid int
		want []int
	}{
		{"the group and all it started", 60, []int{60, 61, 70, 71, 80}},
		{"a group of one", 100, []int{100}},
		{"a group that is gone", 200, nil},
		{"init's group is no job's", 1, nil},
		{"nor is group 0", 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := pids(tree(all, c.pgid)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("tree(%d) = %v, want %v", c.pgid, got, c.want)
			}
		})
	}
}

func TestStatCPU(t *testing.T) {
	for _, c := range []struct {
		stat string
		want uint64
		ok   bool
	}{
		{"60 (make) S 50 60 60 0 -1 4194560 100 0 0 0 1234 56 0 0 20 0 1 0", 1290, true},
		{"61 (go (test)) R 60 60 60 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0", 10, true},
		{"62 (x) S 60 60 60 0", 0, false},
		{"63 (x) S 60 60 60 0 -1 0 0 0 0 0 a 3", 0, false},
		{"no name", 0, false},
	} {
		got, err := statCPU([]byte(c.stat))
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("statCPU(%q) = %d, %v; want %d", c.stat, got, err, c.want)
		}
	}
}

// Linux's table for a tree: every process, whoever's, with its CPU time; zombies left out.
func TestRowsFromProc(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/60/stat":  {Data: []byte("60 (make) S 50 60 60 0 -1 0 0 0 0 0 1234 56 0 0 20 0 1 0")},
		"proc/61/stat":  {Data: []byte("61 (my (odd) cc) R 60 60 60 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0")},
		"proc/62/stat":  {Data: []byte("62 (sh) Z 60 60 60 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0")},
		"proc/63/stat":  {Data: []byte("63 (gone) S 60")}, // read as it ended
		"proc/self/x":   {Data: []byte("")},
		"proc/uptime":   {Data: []byte("1 1\n")},
		"proc/64/other": {Data: []byte("no stat: it ended")},
	}
	got, err := rowsFromProc(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []row{{pid: 60, ppid: 50, pgid: 60, cpu: 1290}, {pid: 61, ppid: 60, pgid: 60, cpu: 10}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rowsFromProc = %+v, want %+v", got, want)
	}
	fsys["proc/65/stat"] = &fstest.MapFile{Data: []byte("65 (x) S 1 65 65 0 -1 0 0 0 0 0 x y 0")}
	if _, err := rowsFromProc(fsys); err == nil {
		t.Error("a process whose CPU time does not read made a reading without it")
	}
}

// Where baton reads no process table, there is no reading of a tree either.
func TestTreeUsageUnsupported(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Skip("baton reads the process table here")
	}
	if _, err := TreeUsage(100); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("TreeUsage on %s = %v", runtime.GOOS, err)
	}
}
