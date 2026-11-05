package procs

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

// The test binary stands in for a process a session left running: run with PROCS_HELPER set, it sleeps
// (and, as "stubborn", ignores SIGTERM).
func TestMain(m *testing.M) {
	switch os.Getenv("PROCS_HELPER") {
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helper starts the test binary as a process left running, in dir, with the environment's BATON_* and
// extra variables, and reaps it when it ends.
func helper(t *testing.T, kind, dir string, extra ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), append([]string{"PROCS_HELPER=" + kind}, extra...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-done
	})
	return cmd
}

// procargsOf is kern.procargs2 as macOS gives it for a process of argc arguments started from path, strs its
// arguments and then its environment.
func procargsOf(argc int, path string, strs ...string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(argc))
	b = append(b, path...)
	b = append(b, 0, 0, 0, 0) // the path's NUL, then padding
	for _, s := range strs {
		b = append(append(b, s...), 0)
	}
	return b
}

func TestProcargs(t *testing.T) {
	// the background shell Claude Code 2.1.318 starts for a command of its model's (spike 45), cut short
	const script = "source /Users/me/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'until [ -f x ]; do sleep 5; done' < /dev/null && pwd -P >| /tmp/claude-8214-cwd"
	for _, c := range []struct {
		name      string
		b         []byte
		args, env []string
	}{
		{"arguments then the environment", procargsOf(2, "/usr/bin/node", "node", "server.js", "PATH=/bin", "BATON_DIR=/p/.baton", "", "ptr_munge="),
			[]string{"node", "server.js"}, []string{"PATH=/bin", "BATON_DIR=/p/.baton"}},
		{"an empty argument", procargsOf(3, "/x", "x", "", "y", "HOME=/h", ""), []string{"x", "", "y"}, []string{"HOME=/h"}},
		{"hidden: a platform binary's environment, not its arguments", procargsOf(2, "/bin/sleep", "sleep", "3600", ""), []string{"sleep", "3600"}, nil},
		{"a background shell's script", procargsOf(3, "/bin/zsh", "/bin/zsh", "-c", script, ""), []string{"/bin/zsh", "-c", script}, nil},
		{"no closing string", procargsOf(1, "/x", "x", "A=1"), []string{"x"}, []string{"A=1"}},
		{"no arguments", procargsOf(0, "/x", "A=1", ""), nil, []string{"A=1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args, env, err := procargs(c.b)
			if err != nil || !reflect.DeepEqual(args, c.args) || !reflect.DeepEqual(env, c.env) {
				t.Errorf("procargs = %q, %q, %v; want %q, %q", args, env, err, c.args, c.env)
			}
		})
	}
	for _, b := range [][]byte{nil, {1, 0}, {1, 0, 0, 0, 'x'}} {
		if _, _, err := procargs(b); err == nil {
			t.Errorf("procargs(%q) took it", b)
		}
	}
}

func TestCmdline(t *testing.T) {
	for _, c := range []struct {
		b    string
		want []string
	}{
		{"/bin/zsh\x00-c\x00until [ -f x ]; do sleep 5; done\x00", []string{"/bin/zsh", "-c", "until [ -f x ]; do sleep 5; done"}},
		{"node\x00\x00server.js\x00", []string{"node", "", "server.js"}},   // an empty argument
		{"postgres: writer process", []string{"postgres: writer process"}}, // rewritten by the process
		{"", nil}, // a kernel thread's
	} {
		if got := cmdline([]byte(c.b)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("cmdline(%q) = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestFromProc(t *testing.T) {
	proc := func(stat, uid, environ string, cwd string) map[string]*fstest.MapFile {
		return map[string]*fstest.MapFile{
			"stat":    {Data: []byte(stat)},
			"status":  {Data: []byte("Name:\tx\nUid:\t" + uid + "\t" + uid + "\t" + uid + "\t" + uid + "\n")},
			"environ": {Data: []byte(environ)},
			"cwd":     {Data: []byte(cwd), Mode: fs.ModeSymlink},
		}
	}
	fsys := fstest.MapFS{}
	add := func(pid string, files map[string]*fstest.MapFile) {
		for name, f := range files {
			fsys["proc/"+pid+"/"+name] = f
		}
	}
	add("100", proc("100 (node) S 1 90 90 0", "501", "PATH=/bin\x00BATON_DIR=/p/.baton\x00BATON_INSTANCE=abc-P1\x00", "/h/.baton/worktrees/k/r/P1 (deleted)"))
	add("101", proc("101 (my (odd) name) S 100 90 90 0", "501", "", "/p"))
	fsys["proc/100/cmdline"] = &fstest.MapFile{Data: []byte("node\x00server.js\x00")}
	fsys["proc/101/cmdline"] = &fstest.MapFile{Data: []byte("/bin/zsh\x00-c\x00until [ -f x ]; do sleep 5; done\x00")}
	add("102", proc("102 (sleep) Z 100 90 90 0", "501", "", "/"))
	add("103", proc("103 (sshd) S 1 103 103 0", "0", "BATON_DIR=/p/.baton\x00", "/"))
	add("104", proc("104 (go) S 1 104 104 0", "501", "BATON_DIR=/p/.baton\x00", "/me")) // this process
	fsys["proc/uptime"] = &fstest.MapFile{Data: []byte("1 1\n")}
	delete(fsys, "proc/101/environ") // unreadable

	got, err := fromProc(fsys, 501, 104)
	if err != nil {
		t.Fatal(err)
	}
	want := []Proc{
		{PID: 100, PPID: 1, PGID: 90, Comm: "node", Cwd: "/h/.baton/worktrees/k/r/P1", Env: map[string]string{"BATON_DIR": "/p/.baton", "BATON_INSTANCE": "abc-P1"}, Args: []string{"node", "server.js"}},
		{PID: 101, PPID: 100, PGID: 90, Comm: "my (odd) name", Cwd: "/p", Args: []string{"/bin/zsh", "-c", "until [ -f x ]; do sleep 5; done"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fromProc =\n%+v\nwant\n%+v", got, want)
	}
	if !got[1].Hidden() || got[0].Hidden() {
		t.Error("Hidden is an environment that could not be read")
	}

	for _, c := range []struct {
		pid, ppid int
		comm      string
		err       bool
	}{
		{100, 1, "node", false},
		{101, 100, "my (odd) name", false},
		{999, 0, "", true}, // gone
	} {
		ppid, comm, err := parentFromProc(fsys, c.pid)
		if ppid != c.ppid || comm != c.comm || (err != nil) != c.err {
			t.Errorf("parentFromProc(%d) = %d, %q, %v", c.pid, ppid, comm, err)
		}
	}
}

// Where baton reads no process table, List says so.
func TestListUnsupported(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Skip("baton reads the process table here")
	}
	if _, err := List(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("List on %s = %v", runtime.GOOS, err)
	}
	if _, _, err := Parent(os.Getpid()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Parent on %s = %v", runtime.GOOS, err)
	}
}
