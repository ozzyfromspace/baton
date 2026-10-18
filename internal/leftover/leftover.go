// Package leftover finds what a phase left running once its work is over (v0.8.7): processes whose
// working directory is inside its worktree or a lane it used, and docker containers a compose project
// there started. A real run's phase agent kept its own test stack up after its work, beside the main one,
// and the QA gate competed with it for the machine: the rule that a phase stops what it started lived only
// in model judgment. baton clean, a phase agent's `baton done` (asked once) and retiring a worker (which
// stops its processes) read the machine through it:
//
//   - the process table with package procs, as baton clean reads it: no cgo, ps or lsof. A process counts
//     as a phase's when its environment holds a BATON_ variable (a hosted session, a phase agent or a gate
//     started it); one whose environment is hidden (macOS hides a platform binary's: sh, zsh, sleep)
//     counts only in the process group of such a process, since alone it may be the human's own shell; one
//     whose environment holds none is the human's. The command that looks (its ancestors and its process
//     group) and the jobs still running (their runners and process groups) are left out (Skip).
//   - docker, when its CLI is on the PATH: `docker ps -a` lists the containers compose started, and those
//     whose com.docker.compose.project.working_dir label is inside a phase's directory are its own. With
//     no docker, or none that answers, there are none.
//
// Paths are compared as strings, in both their spelling and their resolved one (Spellings): a working
// directory that was removed cannot be resolved, and macOS reads a process's working directory resolved
// (/private/var for /var).
package leftover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/procs"
)

// Container is a docker container a compose project started.
type Container struct {
	ID     string
	Name   string
	Status string // as docker ps says it: "Up 3 minutes", "Exited (0) 2 hours ago"
	// Project is its compose project, and Dir the directory compose ran in (its labels).
	Project string
	Dir     string
}

// Running reports a container that runs (or is paused, still holding its memory).
func (c Container) Running() bool { return strings.HasPrefix(c.Status, "Up") }

// Finder reads the machine. A nil field reads nothing, or stops nothing: tests give fakes, and only
// Machine reads this machine.
type Finder struct {
	Procs      func() ([]procs.Proc, error)
	Containers func(ctx context.Context) ([]Container, error)
	// Stop stops each process on its own, never its process group, and returns those still running.
	Stop func(pids []int) []int
}

// StopGrace is how long a process asked to stop (SIGTERM) has before it is killed.
const StopGrace = 5 * time.Second

// Machine is the finder that reads this machine: its process table (procs.List, none on Windows), docker's
// containers when the docker CLI is on the PATH (Docker), and procs.Stop with StopGrace.
func Machine() Finder {
	return Finder{Procs: procs.List, Containers: Docker, Stop: func(pids []int) []int { return procs.Stop(pids, StopGrace) }}
}

// Left is what was found running in a phase's directories.
type Left struct {
	// Procs are the processes started under baton (a BATON_ variable in their environment); Hidden those
	// whose environment is hidden, each in the process group of one of Procs.
	Procs, Hidden []procs.Proc
	Containers    []Container
	// Notes say what could not be looked at, and why: the process table, or docker.
	Notes []string
}

// Empty reports that nothing was found running.
func (l Left) Empty() bool { return len(l.Procs)+len(l.Hidden)+len(l.Containers) == 0 }

// In is what of l works in dir, or inside it.
func (l Left) In(dir string) Left {
	where := Spellings(dir)
	in := func(p procs.Proc) bool { return !Under(p.Cwd, where) }
	return Left{Procs: slices.DeleteFunc(slices.Clone(l.Procs), in), Hidden: slices.DeleteFunc(slices.Clone(l.Hidden), in),
		Containers: slices.DeleteFunc(slices.Clone(l.Containers), func(c Container) bool { return !Under(c.Dir, where) })}
}

// Running is l with only the containers that run.
func (l Left) Running() Left {
	l.Containers = slices.DeleteFunc(slices.Clone(l.Containers), func(c Container) bool { return !c.Running() })
	return l
}

// Skip names the processes that are never a phase's leftovers: the command that looks, its parent and
// theirs, and its process group; and the jobs still running, by their runners and their process groups.
type Skip struct {
	Parent int // the parent of the process that looks (0: none)
	Group  int // its process group (0 or 1: none)
	PIDs   map[int]bool
	Groups map[int]bool
}

// Here is the Skip of this process: its parent's line and its process group.
func Here() Skip { return Skip{Parent: os.Getppid(), Group: procs.Group()} }

// Job leaves out a job's runner (pid) and its process group (pgid); 0 for either is none.
func (s *Skip) Job(pid, pgid int) {
	if pid > 0 {
		if s.PIDs == nil {
			s.PIDs = map[int]bool{}
		}
		s.PIDs[pid] = true
	}
	if pgid > 0 {
		if s.Groups == nil {
			s.Groups = map[int]bool{}
		}
		s.Groups[pgid] = true
	}
}

// Of is the test s makes of a process of table.
func (s Skip) Of(table []procs.Proc) func(procs.Proc) bool {
	mine := Ancestors(table, s.Parent)
	return func(p procs.Proc) bool {
		return s.PIDs[p.PID] || s.Groups[p.PGID] || mine[p.PID] || s.Group > 1 && p.PGID == s.Group
	}
}

// Ancestors are pid and its parent, and theirs, as table records them.
func Ancestors(table []procs.Proc, pid int) map[int]bool {
	parents := map[int]int{}
	for _, p := range table {
		parents[p.PID] = p.PPID
	}
	out := map[int]bool{}
	for ; pid > 1 && !out[pid]; pid = parents[pid] {
		out[pid] = true
	}
	return out
}

// Find looks for what runs in dirs (a phase's worktree and lanes), leaving out what skip names. What could
// not be looked at is in the result's notes.
func (f Finder) Find(ctx context.Context, dirs []string, skip Skip) Left {
	var l Left
	where := Spellings(dirs...)
	if len(where) == 0 {
		return l
	}
	if f.Procs != nil {
		switch table, err := f.Procs(); {
		case errors.Is(err, procs.ErrUnsupported):
			l.Notes = append(l.Notes, "processes: "+err.Error())
		case err != nil:
			l.Notes = append(l.Notes, "processes: the process table could not be read: "+err.Error())
		default:
			l.Procs, l.Hidden = Working(table, where, skip.Of(table))
		}
	}
	if f.Containers != nil {
		cs, err := f.Containers(ctx)
		if err != nil {
			l.Notes = append(l.Notes, "containers: "+err.Error())
		}
		for _, c := range cs {
			if Under(c.Dir, where) {
				l.Containers = append(l.Containers, c)
			}
		}
	}
	return l
}

// Working sorts the processes of table that work in where (Spellings) and skip leaves in: those started
// under baton (a BATON_ variable in their environment), and those whose environment is hidden in the
// process group of one of them. Any other is the human's own, and left out.
func Working(table []procs.Proc, where []string, skip func(procs.Proc) bool) (ours, hidden []procs.Proc) {
	groups := map[int]bool{}
	var maybe []procs.Proc
	for _, p := range table {
		if skip(p) || !Under(p.Cwd, where) {
			continue
		}
		switch {
		case len(p.Env) > 0:
			ours = append(ours, p)
			groups[p.PGID] = true
		case p.Hidden():
			maybe = append(maybe, p)
		}
	}
	for _, p := range maybe {
		if groups[p.PGID] {
			hidden = append(hidden, p)
		}
	}
	return ours, hidden
}

// PIDs are the processes' ids, for an event's fields.
func PIDs(ps []procs.Proc) []int {
	out := []int{}
	for _, p := range ps {
		out = append(out, p.PID)
	}
	return out
}

// StopAll stops ps, each on its own (Finder.Stop), and says which it stopped and which still run. With no
// Stop, it stops none.
func (f Finder) StopAll(ps []procs.Proc) (stopped, left []procs.Proc) {
	if len(ps) == 0 {
		return nil, nil
	}
	if f.Stop == nil {
		return nil, ps
	}
	var pids []int
	for _, p := range ps {
		pids = append(pids, p.PID)
	}
	still := f.Stop(pids)
	for _, p := range ps {
		if slices.Contains(still, p.PID) {
			left = append(left, p)
		} else {
			stopped = append(stopped, p)
		}
	}
	return stopped, left
}

// Still is those of ps that run still: the table read again has each, by its pid and name. With no table
// to read, all of them.
func (f Finder) Still(ps []procs.Proc) []procs.Proc {
	if len(ps) == 0 || f.Procs == nil {
		return ps
	}
	table, err := f.Procs()
	if err != nil {
		return ps
	}
	return slices.DeleteFunc(slices.Clone(ps), func(p procs.Proc) bool {
		return !slices.ContainsFunc(table, func(q procs.Proc) bool { return q.PID == p.PID && q.Comm == p.Comm })
	})
}

// WorkingDirLabel is the label compose puts on each container it starts: the directory it ran in.
const WorkingDirLabel = "com.docker.compose.project.working_dir"

// DockerTimeout bounds `docker ps`: a daemon that does not answer has no containers to give.
const DockerTimeout = 5 * time.Second

// dockerFormat is a line of `docker ps` for each container: its id, name, status, compose project and
// working directory, between tabs.
const dockerFormat = `{{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Label "com.docker.compose.project"}}\t{{.Label "` + WorkingDirLabel + `"}}`

// Docker lists the containers compose started (`docker ps -a`, those with a WorkingDirLabel), running or
// not. With no docker CLI on the PATH there are none, and no error.
func Docker(ctx context.Context) ([]Container, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, DockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+WorkingDirLabel, "--format", dockerFormat)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		why := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(why, '\n'); i >= 0 {
			why = why[:i]
		}
		if why == "" {
			why = err.Error()
		}
		return nil, fmt.Errorf("docker ps: %s", why)
	}
	return ParseDocker(out), nil
}

// ParseDocker reads docker ps's lines in dockerFormat: a line without a working directory is skipped.
func ParseDocker(out []byte) []Container {
	var cs []Container
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[0] == "" || f[4] == "" {
			continue
		}
		cs = append(cs, Container{ID: f[0], Name: f[1], Status: f[2], Project: f[3], Dir: f[4]})
	}
	return cs
}

// Spellings is each of dirs cleaned and resolved, both: what a path that names one of them may read as.
func Spellings(dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for _, s := range []string{filepath.Clean(d), Resolved(d)} {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// Under reports a path inside (or equal to) one of dirs, compared as strings: a removed working directory
// cannot be resolved.
func Under(path string, dirs []string) bool {
	if path == "" {
		return false
	}
	path = filepath.Clean(path)
	for _, d := range dirs {
		if d != "" && d != "." && (path == d || strings.HasPrefix(path, d+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// Resolved is path with its symbolic links resolved, or cleaned when it is gone.
func Resolved(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
