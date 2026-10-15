package hostcheck

import (
	"errors"
	"os"
	"testing"

	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// proc is one process of a fake table: its parent and its name.
type proc struct {
	ppid int
	name string
}

// table reads a fake process table, counting the reads.
func table(ps map[int]proc, reads *int) Reader {
	return func(pid int) (int, string, error) {
		*reads++
		p, ok := ps[pid]
		if !ok {
			return 0, "", errors.New("no such process")
		}
		return p.ppid, p.name, nil
	}
}

func TestCheck(t *testing.T) {
	// A hosted session: the hook (40) under a shell (30) under claude (20) under the host (10). The
	// background service's session: the hook (41) under claude (21) under bg-pty-host (11) under the
	// daemon (5), started by launchd (1). Another host (12) runs elsewhere.
	ps := map[int]proc{
		40: {30, "baton"}, 30: {20, "sh"}, 20: {10, "claude"}, 10: {9, "baton"}, 9: {1, "zsh"},
		41: {21, "baton"}, 21: {11, "claude"}, 11: {5, "claude"}, 5: {1, "claude"},
		12: {1, "baton"},
		1:  {0, "launchd"},
	}
	hosted := func(dir string) map[string]string {
		return map[string]string{"BATON_HOST": "1", "BATON_DIR": dir, "BATON_INSTANCE": "inst"}
	}
	for _, c := range []struct {
		name   string
		env    map[string]string
		pid    int  // the host's recorded process; 0 records none
		self   int  // the process asking
		broken bool // the table cannot be read
		want   Status
	}{
		{"not baton's", map[string]string{}, 10, 40, false, Plain},
		{"its host is an ancestor", nil, 10, 40, false, Hosted},
		{"its host is alive but not an ancestor", nil, 10, 41, false, Inherited},
		{"another host is alive, not an ancestor", nil, 12, 41, false, Inherited},
		{"no host above it, none recorded", nil, 0, 41, false, Inherited},
		{"none recorded, a baton above it (an older host)", nil, 0, 40, false, Hosted},
		{"a recorded host that is gone", nil, 77, 41, false, Inherited},
		{"no BATON_DIR, a baton above it", map[string]string{"BATON_HOST": "1"}, 0, 40, false, Hosted},
		{"no BATON_DIR, none above it", map[string]string{"BATON_HOST": "1"}, 0, 41, false, Inherited},
		{"the table cannot be read", nil, 10, 41, true, Hosted},
		{"the asking process is gone", nil, 10, 99, false, Hosted},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := state.OpenProject(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if c.pid != 0 {
				if err := p.SetHostPID("inst", c.pid); err != nil {
					t.Fatal(err)
				}
			}
			env := c.env
			if env == nil {
				env = hosted(p.Dir)
			}
			reads := 0
			read := table(ps, &reads)
			if c.broken {
				read = func(int) (int, string, error) { return 0, "", procs.ErrUnsupported }
			}
			if got := Check(func(k string) string { return env[k] }, c.self, read); got != c.want {
				t.Errorf("Check = %v, want %v", got, c.want)
			}
			if reads > 6 {
				t.Errorf("read %d processes for a chain 5 deep", reads)
			}
		})
	}
}

// With nothing to read processes with, the variable alone says, as before v0.8.4.
func TestTrust(t *testing.T) {
	for env, want := range map[string]Status{"1": Hosted, "": Plain, "0": Plain} {
		if got := Trust(func(k string) string { return map[string]string{"BATON_HOST": env}[k] }); got != want {
			t.Errorf("BATON_HOST=%q: %v, want %v", env, got, want)
		}
	}
}

// On the machine's own processes: a host recorded as this process is no ancestor of it, and its parent is.
func TestOf(t *testing.T) {
	if _, _, err := procs.Parent(os.Getpid()); errors.Is(err, procs.ErrUnsupported) {
		t.Skip("baton reads no processes here")
	}
	p, err := state.OpenProject(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BATON_HOST": "1", "BATON_DIR": p.Dir, "BATON_INSTANCE": "inst"}
	for pid, want := range map[int]Status{os.Getpid(): Inherited, os.Getppid(): Hosted} {
		p.SetHostPID("inst", pid)
		if got := Of(func(k string) string { return env[k] }); got != want {
			t.Errorf("host %d: Of = %v, want %v", pid, got, want)
		}
	}
}
