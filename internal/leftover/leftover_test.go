package leftover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/procs"
)

// baton is the environment of a process started under baton: a hosted session's, a phase agent's, a gate's.
var baton = map[string]string{"BATON_INSTANCE": "inst", "BATON_DIR": "/p/.baton"}

// fake is a finder over table and containers, whose Stop records what it stops and cannot stop those in
// stubborn.
type fake struct {
	table      []procs.Proc
	containers []Container
	dockerErr  error
	stopped    []int
	stubborn   map[int]bool
}

func (x *fake) finder() Finder {
	return Finder{
		Procs: func() ([]procs.Proc, error) {
			return slices.DeleteFunc(slices.Clone(x.table), func(p procs.Proc) bool { return slices.Contains(x.stopped, p.PID) }), nil
		},
		Containers: func(context.Context) ([]Container, error) { return x.containers, x.dockerErr },
		Stop: func(pids []int) []int {
			var left []int
			for _, pid := range pids {
				if x.stubborn[pid] {
					left = append(left, pid)
					continue
				}
				x.stopped = append(x.stopped, pid)
			}
			return left
		},
	}
}

// A phase's processes are those started under baton that work in its directories, and those whose
// environment is hidden in the process group of one of them. The human's own (no BATON_ variable, or hidden
// alone, as their shell is on macOS), what works elsewhere or beside the worktree under a name that starts
// the same, the command that looks and the jobs still running are left out.
func TestWhatAPhaseLeftRunning(t *testing.T) {
	tree, lane := filepath.Join(t.TempDir(), "P3"), filepath.Join(t.TempDir(), "lanes", "1")
	x := &fake{table: []procs.Proc{
		{PID: 101, PPID: 1, PGID: 101, Comm: "node", Cwd: filepath.Join(tree, "web"), Env: baton},              // a dev server in the worktree
		{PID: 102, PPID: 101, PGID: 101, Comm: "sh", Cwd: tree},                                                // hidden, in its group
		{PID: 103, PPID: 1, PGID: 103, Comm: "zsh", Cwd: tree},                                                 // hidden alone: the human's shell
		{PID: 104, PPID: 1, PGID: 104, Comm: "vim", Cwd: tree, Env: map[string]string{}},                       // no BATON_ variable: the human's
		{PID: 105, PPID: 1, PGID: 105, Comm: "node", Cwd: tree + "-other", Env: baton},                         // beside the worktree
		{PID: 106, PPID: 1, PGID: 106, Comm: "postgres", Cwd: lane, Env: map[string]string{"BATON_LANE": "1"}}, // a gate's, in a lane
		{PID: 107, PPID: 1, PGID: 107, Comm: "node", Cwd: "/elsewhere", Env: baton},                            // elsewhere
		{PID: 108, PPID: 1, PGID: 108, Comm: "node", Env: baton},                                               // its directory cannot be read
		{PID: 200, PPID: 1, PGID: 200, Comm: "baton", Cwd: tree, Env: baton},                                   // a job's runner
		{PID: 201, PPID: 200, PGID: 201, Comm: "go", Cwd: tree, Env: baton},                                    // and its gate's group
		{PID: 300, PPID: 1, PGID: 300, Comm: "zsh", Cwd: tree, Env: baton},                                     // the shell that looks
		{PID: 301, PPID: 300, PGID: 301, Comm: "baton", Cwd: tree, Env: baton},                                 // its parent: the command that looks
		{PID: 302, PPID: 300, PGID: 301, Comm: "cat", Cwd: tree, Env: baton},                                   // in the command's own group
	}, containers: []Container{
		{ID: "a1", Name: "p3-db-1", Status: "Up 3 minutes", Project: "p3", Dir: tree},
		{ID: "a2", Name: "p3-web-1", Status: "Exited (0) 2 minutes ago", Project: "p3", Dir: tree},
		{ID: "a3", Name: "main-db-1", Status: "Up 2 hours", Project: "main", Dir: "/work/checkout"},
		{ID: "a4", Name: "other-db-1", Status: "Up 2 hours", Project: "other", Dir: tree + "-other"},
		{ID: "a5", Name: "lane-db-1", Status: "Up 1 minute", Project: "lane", Dir: filepath.Join(lane, "deploy")},
	}}
	skip := Skip{Parent: 301, Group: 301}
	skip.Job(200, 201)
	got := x.finder().Find(context.Background(), []string{tree, lane}, skip)
	if !reflect.DeepEqual(PIDs(got.Procs), []int{101, 106}) || !reflect.DeepEqual(PIDs(got.Hidden), []int{102}) {
		t.Errorf("processes %v, hidden %v; want [101 106], [102]", PIDs(got.Procs), PIDs(got.Hidden))
	}
	var names []string
	for _, c := range got.Containers {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"p3-db-1", "p3-web-1", "lane-db-1"}) || len(got.Notes) > 0 || got.Empty() {
		t.Errorf("containers %v, notes %v", names, got.Notes)
	}
	if in := got.In(tree); !reflect.DeepEqual(PIDs(in.Procs), []int{101}) || len(in.Hidden) != 1 || len(in.Containers) != 2 {
		t.Errorf("in the worktree: %+v", in)
	}
	if run := got.Running(); len(run.Containers) != 2 || run.Containers[1].Name != "lane-db-1" || len(got.Containers) != 3 {
		t.Errorf("running: %+v (all: %d)", run.Containers, len(got.Containers))
	}

	// With no docker there are no containers; with none that answers, a note says why.
	f := x.finder()
	f.Containers = nil
	if got := f.Find(context.Background(), []string{tree}, Skip{}); len(got.Containers) > 0 || len(got.Notes) > 0 {
		t.Errorf("no docker: %+v", got)
	}
	x.dockerErr, x.containers = errors.New("docker ps: Cannot connect to the Docker daemon"), nil
	if got := x.finder().Find(context.Background(), []string{tree}, Skip{}); len(got.Containers) > 0 || len(got.Notes) != 1 ||
		!strings.Contains(got.Notes[0], "Cannot connect") {
		t.Errorf("docker down: %+v", got)
	}
	// With no process table, a note says so; with no directories, nothing is read.
	f = x.finder()
	f.Procs = func() ([]procs.Proc, error) { return nil, procs.ErrUnsupported }
	if got := f.Find(context.Background(), []string{tree}, Skip{}); len(got.Procs) > 0 || len(got.Notes) != 2 || !strings.HasPrefix(got.Notes[0], "processes: ") {
		t.Errorf("no process table: %+v", got)
	}
	f.Procs = func() ([]procs.Proc, error) { t.Error("read the table with nowhere to look"); return nil, nil }
	if got := f.Find(context.Background(), []string{""}, Skip{}); !got.Empty() {
		t.Errorf("nowhere: %+v", got)
	}
	if got := (Finder{}).Find(context.Background(), []string{tree}, Skip{}); !got.Empty() || len(got.Notes) > 0 {
		t.Errorf("the zero finder found %+v", got)
	}
}

// Stopping stops each process on its own, and says which still run; with no Stop, none is stopped. Still
// keeps those the table still has, by pid and name.
func TestStoppingWhatWasLeft(t *testing.T) {
	x := &fake{table: []procs.Proc{{PID: 1, Comm: "node"}, {PID: 2, Comm: "go"}, {PID: 3, Comm: "sh"}, {PID: 4, Comm: "sleep"}},
		stubborn: map[int]bool{2: true}}
	f := x.finder()
	stopped, left := f.StopAll(x.table[:2])
	if !reflect.DeepEqual(PIDs(stopped), []int{1}) || !reflect.DeepEqual(PIDs(left), []int{2}) || !reflect.DeepEqual(x.stopped, []int{1}) {
		t.Errorf("stopped %v, left %v, asked %v", PIDs(stopped), PIDs(left), x.stopped)
	}
	if s, l := f.StopAll(nil); s != nil || l != nil {
		t.Errorf("stopped %v %v of nothing", s, l)
	}
	if still := f.Still([]procs.Proc{{PID: 1, Comm: "node"}, {PID: 3, Comm: "sh"}, {PID: 4, Comm: "node"}}); !reflect.DeepEqual(PIDs(still), []int{3}) {
		t.Errorf("still %v, want [3]: 1 was stopped, and 4 is another program now", PIDs(still))
	}
	f.Stop = nil
	if s, l := f.StopAll(x.table[2:]); len(s) != 0 || !reflect.DeepEqual(PIDs(l), []int{3, 4}) {
		t.Errorf("with no Stop: stopped %v, left %v", PIDs(s), PIDs(l))
	}
	if still := (Finder{}).Still(x.table[2:]); len(still) != 2 {
		t.Errorf("with no table: still %v", PIDs(still))
	}
}

// Ancestors follow the parents of a pid up to init, and stop at a loop.
func TestAncestors(t *testing.T) {
	table := []procs.Proc{{PID: 10, PPID: 5}, {PID: 5, PPID: 1}, {PID: 7, PPID: 8}, {PID: 8, PPID: 7}}
	for _, c := range []struct {
		pid  int
		want []int
	}{{10, []int{5, 10}}, {7, []int{7, 8}}, {0, nil}, {1, nil}, {42, []int{42}}} {
		var got []int
		for pid := range Ancestors(table, c.pid) {
			got = append(got, pid)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Ancestors(%d) = %v, want %v", c.pid, got, c.want)
		}
	}
}

func TestUnderAndSpellings(t *testing.T) {
	for _, c := range []struct {
		path string
		dirs []string
		want bool
	}{
		{"/w/P3", []string{"/w/P3"}, true},
		{"/w/P3/web/", []string{"/w/P3"}, true},
		{"/w/P3-other", []string{"/w/P3"}, false},
		{"/w", []string{"/w/P3"}, false},
		{"", []string{"/w/P3"}, false},
		{"/w/P3", []string{"", "."}, false},
	} {
		var dirs []string
		for _, d := range c.dirs {
			dirs = append(dirs, filepath.FromSlash(d))
		}
		if got := Under(filepath.FromSlash(c.path), dirs); got != c.want {
			t.Errorf("Under(%q, %q) = %v", c.path, c.dirs, got)
		}
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	got := Spellings(link, "", "/gone/dir/")
	want := []string{link, Resolved(real), "/gone/dir"}
	if runtime.GOOS == "windows" {
		want[2] = filepath.Clean("/gone/dir/")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Spellings = %q, want %q", got, want)
	}
}

func TestParseDocker(t *testing.T) {
	out := "a1\tp3-db-1\tUp 3 minutes (healthy)\tp3\t/w/P3\r\n" +
		"a2\tp3-web-1\tExited (0) 2 hours ago\tp3\t/w/P3\n" +
		"a3\tno-dir\tUp 1 minute\tx\t\n" + // no working directory: not compose's
		"garbage\n\n"
	got := ParseDocker([]byte(out))
	want := []Container{{ID: "a1", Name: "p3-db-1", Status: "Up 3 minutes (healthy)", Project: "p3", Dir: "/w/P3"},
		{ID: "a2", Name: "p3-web-1", Status: "Exited (0) 2 hours ago", Project: "p3", Dir: "/w/P3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseDocker = %+v", got)
	}
	if !got[0].Running() || got[1].Running() {
		t.Errorf("running: %v %v", got[0].Running(), got[1].Running())
	}
}

// Docker lists compose's containers through the docker CLI on the PATH; with none there, none and no
// error; one that fails says why, in its own words.
func TestDocker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if cs, err := Docker(context.Background()); cs != nil || err != nil {
		t.Fatalf("no docker: %v, %v", cs, err)
	}
	script := "#!/bin/sh\n" +
		`[ "$1 $2 $3 $4" = "ps -a --filter label=` + WorkingDirLabel + `" ] || { echo "unexpected: $*" >&2; exit 3; }` + "\n" +
		`printf 'a1\tp3-db-1\tUp 3 minutes\tp3\t/w/P3\n'` + "\n"
	os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755)
	cs, err := Docker(context.Background())
	if err != nil || len(cs) != 1 || cs[0].Name != "p3-db-1" || cs[0].Dir != "/w/P3" {
		t.Fatalf("Docker = %+v, %v", cs, err)
	}
	os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\necho second line >&2\nexit 1\n"), 0o755)
	if cs, err := Docker(context.Background()); cs != nil || err == nil || err.Error() != "docker ps: Cannot connect to the Docker daemon" {
		t.Errorf("a docker that fails: %v, %v", cs, err)
	}
}
