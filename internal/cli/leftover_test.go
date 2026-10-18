package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// runWith runs a command in the session with f reading what phases left running.
func (s *session) runWith(f leftover.Finder, args ...string) (int, string, string) {
	io, out, errb := testIO("", s.env)
	io.Now, io.Dir, io.Hosted, io.Leftovers = func() time.Time { return s.now }, s.dir, s.host, f
	code := Main(args, io)
	return code, out.String(), errb.String()
}

// leftFinder reads table and containers, and stops nothing.
func leftFinder(table []procs.Proc, containers ...leftover.Container) leftover.Finder {
	return leftover.Finder{
		Procs:      func() ([]procs.Proc, error) { return table, nil },
		Containers: func(context.Context) ([]leftover.Container, error) { return containers, nil },
	}
}

// A phase agent's `baton done` is refused once while its worktree, or a lane its gates ran in and nothing
// holds, has something of its running: each listed with the command that stops it, and logged
// leftover_asked. The same call again goes through. What is not the phase's (elsewhere, a job of its that
// runs, a lane brought up for the host) asks nothing, and nor does the lead's own done.
func TestAPhaseAgentsDoneAsksOnceAboutWhatItLeftRunning(t *testing.T) {
	env := map[string]string{"BATON_INSTANCE": "inst"}
	for _, c := range []struct {
		name  string
		setup func(w *session, tree string, pool lane.Pool) leftover.Finder
		asked []string // what the refusal names ("" for none: no refusal)
		not   []string
	}{
		{"a dev server and a container in its worktree", func(w *session, tree string, _ lane.Pool) leftover.Finder {
			return leftFinder([]procs.Proc{{PID: 4123, PGID: 4123, Comm: "node", Cwd: filepath.Join(tree, "web"), Env: env}},
				leftover.Container{Name: "p1-db-1", Status: "Up 3 minutes", Dir: tree}, leftover.Container{Name: "main-db-1", Status: "Up", Dir: "/work/main"})
		}, []string{"not done yet — P1 leaves this running, in its worktree (", "process 4123 (node) in ", "web: kill 4123",
			"container p1-db-1 (Up 3 minutes), which compose started in ", "docker rm -f p1-db-1", "run this `baton done` again as it is"}, []string{"main-db-1"}},
		{"nothing of its", func(w *session, tree string, _ lane.Pool) leftover.Finder {
			return leftFinder([]procs.Proc{{PID: 4123, PGID: 4123, Comm: "node", Cwd: "/elsewhere", Env: env}, {PID: 4124, PGID: 4124, Comm: "zsh", Cwd: tree}},
				leftover.Container{Name: "main-db-1", Status: "Up", Dir: tree + "-other"})
		}, nil, nil},
		{"a wait of its that runs", func(w *session, tree string, _ lane.Pool) leftover.Finder {
			w.set(func(x *state.State) {
				x.Jobs = append(x.Jobs, state.Job{ID: "P1.j1", Phase: "P1", Kind: state.JobAwait, Command: "gh run watch", Status: state.JobRunning, PID: 4200, PGID: 4201})
			})
			return leftFinder([]procs.Proc{{PID: 4200, PGID: 4200, Comm: "baton", Cwd: tree, Env: env}, {PID: 4202, PGID: 4201, Comm: "gh", Cwd: tree, Env: env}})
		}, nil, nil},
		{"a lane its gate ran in, and one brought up for the host", func(w *session, tree string, pool lane.Pool) leftover.Finder {
			for n, up := range map[int]string{1: "", 2: "inst"} {
				os.MkdirAll(pool.LaneDir(n), 0o755)
				if up != "" {
					os.WriteFile(filepath.Join(pool.Dir(), "2.up"), []byte(up+"\n"), 0o644)
				}
			}
			w.set(func(x *state.State) {
				for n := 1; n <= 2; n++ {
					x.Jobs = append(x.Jobs, state.Job{ID: "P1.j" + strconv.Itoa(n), Phase: "P1", Kind: state.JobVerify, Command: "make e2e", Status: state.JobPassed, Lane: n})
				}
			})
			gate := map[string]string{"BATON_LANE": "1"}
			return leftFinder([]procs.Proc{{PID: 5001, PGID: 5001, Comm: "postgres", Cwd: pool.LaneDir(1), Env: gate},
				{PID: 5002, PGID: 5002, Comm: "redis", Cwd: pool.LaneDir(2), Env: gate}})
		}, []string{"process 5001 (postgres) in ", ": kill 5001"}, []string{"5002"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead := asWorker(t)
			w.env["BATON_HOME"] = t.TempDir()
			sub := w.store()
			pool := lane.Pool{Home: w.env["BATON_HOME"], Root: sub.Checkout}
			f := c.setup(w, sub.Root, pool)
			code, out, errs := w.runWith(f, "done", "P1", "--notes", "the parser is in parse.go")
			if c.asked == nil {
				if code != 0 || w.logged("leftover_asked", nil) {
					t.Fatalf("asked: exit %d\n%s%s", code, out, errs)
				}
				return
			}
			if code == 0 || w.state().Mode == state.ModeComplete || !w.logged("leftover_asked", map[string]any{"phase": "P1"}) {
				t.Fatalf("not asked: exit %d, mode %s\n%s%s\n%s", code, w.state().Mode, out, errs, w.events())
			}
			for _, want := range c.asked {
				if !strings.Contains(errs, want) {
					t.Errorf("the refusal lacks %q:\n%s", want, errs)
				}
			}
			for _, not := range c.not {
				if strings.Contains(errs, not) {
					t.Errorf("the refusal names %q:\n%s", not, errs)
				}
			}
			if code, out, errs := w.runWith(f, "done", "P1", "--notes", "the parser is in parse.go"); code != 0 || !strings.Contains(out, "baton: P1 done.") {
				t.Errorf("asked again: exit %d\n%s%s", code, out, errs)
			}
			if n := strings.Count(w.events(), `"kind":"leftover_asked"`); n != 1 {
				t.Errorf("asked %d times", n)
			}
			// The lead's own phase works in the checkout, where what runs may be the human's: not asked.
			lf := leftFinder([]procs.Proc{{PID: 6001, PGID: 6001, Comm: "node", Cwd: lead.store().Root, Env: env}})
			if code, out, errs := lead.runWith(lf, "done", "P0"); code != 0 || lead.logged("leftover_asked", nil) {
				t.Errorf("the lead was asked: exit %d\n%s%s", code, out, errs)
			}
		})
	}
}
