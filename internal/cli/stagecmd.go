package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

func init() {
	register("stage", "run one stage of a gate script: stage <name> [--keep-going] [--runner go|pytest] -- <command…>. "+
		"It marks the stage in the gate's log, stops the gate at its first failed stage, and on a rerun runs only what failed", cmdStage)
}

const stageUsage = "usage: baton stage <name> [--keep-going] [--runner go|pytest] -- <command…>"

// stageDrain is how long a stage reads its command's output after the command ended, for what a process it
// left running still writes; past it the stage ends all the same.
const stageDrain = 2 * time.Second

// cmdStage runs one stage of a gate (package lane's gate protocol): it prints the stage's start, runs the
// command with its output passed through line by line, collects what failed (the command's ::baton fail
// lines and its runner's own failure lines), prints how the stage ended, records it in the job's stages file
// ($BATON_STAGES) and exits with the command's code. A stage of a gate that already failed is skipped, and a
// rerun of a red gate ($BATON_RERUN) carries a stage that passed there and narrows one that failed to what
// failed. It acts on no run, and needs no host: outside a job it prints and runs, and records nothing.
//
// A process the command leaves running must write somewhere else than the stage's output: the stage stops
// reading that output stageDrain after its command ends.
func cmdStage(args []string, io IO) int {
	p, err := parseArgs(args, []string{"runner"}, []string{"keep-going"})
	if err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n%s\n", err, stageUsage)
		return 2
	}
	if p.dash != 1 || len(p.pos) < 2 {
		fmt.Fprintln(io.Err, stageUsage)
		return 2
	}
	name := p.pos[0]
	if !lane.ValidStageName(name) {
		fmt.Fprintf(io.Err, "baton: a stage's name is 1 to %d characters with no spaces, not %q\n", lane.MaxStageName, name)
		return 2
	}
	var preset *stagePreset
	if r := p.vals["runner"]; r != "" {
		pr, ok := stagePresets[r]
		if !ok {
			fmt.Fprintf(io.Err, "baton: --runner is go or pytest, not %q\n", r)
			return 2
		}
		preset = &pr
	}
	s := &stageRun{name: name, argv: p.pos[1:], keepGoing: p.bools["keep-going"], preset: preset, io: io,
		stages: io.Env(lane.EnvStages), commit: io.Env("BATON_COMMIT")}
	return s.run()
}

// stageRun is one run of `baton stage`.
type stageRun struct {
	name      string
	argv      []string
	keepGoing bool
	preset    *stagePreset
	io        IO
	stages    string // the job's stages file ("": outside a job)
	commit    string

	mu      sync.Mutex
	open    bool // the output so far ends mid-line
	stopped bool // the stage no longer reads its command's output
	ids     []string
	over    bool // more than lane.MaxFailures ids
}

func (s *stageRun) run() int {
	if s.stages != "" {
		done, err := lane.ReadStages(s.stages)
		if err != nil {
			fmt.Fprintf(s.io.Err, "baton: %v\n", err)
		}
		if failed, ok := lane.Halted(done); ok {
			s.say(decide.StageHalted(s.name, failed))
			s.say(lane.EndMarker(s.name, lane.StageSkipped, 1, 0))
			s.record(lane.Stage{Name: s.name, Status: lane.StageSkipped, Exit: 1, Commit: s.commit, KeepGoing: s.keepGoing})
			return 1
		}
	}
	prior, from := s.inRedGate()
	var narrow []string
	switch {
	case prior != nil && (prior.Status == lane.StagePassed || prior.Status == lane.StageCarried):
		if prior.Status == lane.StageCarried && prior.From != "" {
			from = prior.From
		}
		s.say(decide.StageCarried(s.name, from, prior.Commit))
		s.say(lane.EndMarker(s.name, lane.StageCarried, 0, 0))
		s.record(lane.Stage{Name: s.name, Status: lane.StageCarried, Commit: prior.Commit, From: from, KeepGoing: s.keepGoing})
		return 0
	case prior != nil && prior.Status == lane.StageFailed:
		narrow = prior.Failures
	}

	env, cleanup, err := s.env(narrow)
	if err != nil {
		fmt.Fprintf(s.io.Err, "baton: %v; stage %s runs whole\n", err, s.name)
		narrow = nil
	}
	defer cleanup()
	started := s.io.Now()
	s.say(decide.StageStart(s.name, started, narrow, from))
	s.say(lane.StartMarker(s.name))
	exit := s.command(env)
	ended := s.io.Now()

	st := lane.Stage{Name: s.name, Status: lane.StagePassed, Exit: exit, Started: started, Ended: ended, Commit: s.commit, KeepGoing: s.keepGoing}
	if exit != 0 {
		st.Status = lane.StageFailed
		s.mu.Lock()
		if !s.over {
			st.Failures = s.ids
		}
		s.mu.Unlock()
	}
	s.say(decide.StageEnd(s.name, exit, st.Took()))
	s.say(lane.EndMarker(s.name, st.Status, exit, st.Took()))
	s.record(st)
	return exit
}

// inRedGate is how this stage ended in the red gate a rerun stands in for ($BATON_RERUN), and that gate's
// job; nil when this is no rerun or the stage did not end there.
func (s *stageRun) inRedGate() (*lane.Stage, string) {
	path := s.io.Env(lane.EnvRerun)
	if path == "" {
		return nil, ""
	}
	from := state.JobFileID(filepath.Base(path))
	red, err := lane.ReadStages(path)
	if err != nil {
		fmt.Fprintf(s.io.Err, "baton: %v; stage %s runs whole\n", err, s.name)
		return nil, from
	}
	for i := len(red) - 1; i >= 0; i-- {
		if red[i].Name == s.name {
			return &red[i], from
		}
	}
	return nil, from
}

// env is the command's environment: this process's, and when the stage is narrowed to ids, the file of them
// (lane.EnvFailed), their pattern (lane.EnvFailedRE) and the runner's own narrowing. cleanup removes the
// file.
func (s *stageRun) env(ids []string) (env []string, cleanup func(), err error) {
	env, cleanup = os.Environ(), func() {}
	if len(ids) == 0 {
		return env, cleanup, nil
	}
	f, err := os.CreateTemp("", "baton-failed-*")
	if err != nil {
		return env, cleanup, err
	}
	_, err = f.WriteString(strings.Join(ids, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return env, cleanup, err
	}
	set := [][2]string{{lane.EnvFailed, f.Name()}, {lane.EnvFailedRE, lane.FailedRE(ids)}}
	if s.preset != nil {
		k, v := s.preset.narrow(ids)
		if was := s.io.Env(k); was != "" {
			v = was + " " + v
		}
		set = append(set, [2]string{k, v})
	}
	for _, kv := range set {
		env = append(withoutVar(env, kv[0]), kv[0]+"="+kv[1])
	}
	return env, func() { os.Remove(f.Name()) }, nil
}

// command runs the stage's command to its end, its output and errors passed through to the stage's output a
// line at a time, and returns its exit code. The stage outlives a stop its command's group is sent (a
// terminal's Ctrl-C, a lane's SIGTERM), so that it says how the command ended.
func (s *stageRun) command(env []string) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	pr, pw, err := os.Pipe()
	if err != nil {
		s.say(decide.StageNoRun(s.name, err))
		return 127
	}
	defer pr.Close()
	cmd := exec.Command(s.argv[0], s.argv[1:]...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, s.io.In, pw, pw
	err = cmd.Start()
	pw.Close()
	if err != nil {
		s.say(decide.StageNoRun(s.name, err))
		return 127
	}
	read := make(chan struct{})
	go func() {
		s.pump(pr)
		close(read)
	}()
	cmd.Wait()
	select {
	case <-read:
	case <-time.After(stageDrain):
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
	}
	return exitCode(cmd.ProcessState)
}

// exitCode is how a command ended as a shell says it: its exit code, or 128 and the signal that killed it.
func exitCode(ps *os.ProcessState) int {
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}

// pump passes the command's output through, a line at a time, and reads each whole line for a failure.
func (s *stageRun) pump(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	mid := false // the chunk before ended mid-line
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			s.pass(chunk, !mid)
			mid = !bytes.HasSuffix(chunk, []byte("\n"))
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// pass writes chunk of the command's output to the stage's output, and when it is a line from its start,
// takes what failed from it.
func (s *stageRun) pass(chunk []byte, whole bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.io.Out.Write(chunk)
	s.open = !bytes.HasSuffix(chunk, []byte("\n"))
	if !whole {
		return
	}
	line := string(chunk)
	id := ""
	if m, ok := lane.ParseMarker(line); ok && m.Kind == lane.MarkFail {
		id = m.ID
	} else if s.preset != nil {
		id = s.preset.failure(strings.TrimRight(line, "\r\n"))
	}
	if id == "" || s.over || slices.Contains(s.ids, id) {
		return
	}
	if len(s.ids) == lane.MaxFailures {
		s.over, s.ids = true, nil // too many to narrow a rerun to: it runs whole
		return
	}
	s.ids = append(s.ids, id)
}

// say prints one of the stage's own lines, on a line of its own.
func (s *stageRun) say(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		fmt.Fprintln(s.io.Out)
		s.open = false
	}
	fmt.Fprintln(s.io.Out, line)
}

// record appends the stage to the job's stages file; outside a job it records nothing.
func (s *stageRun) record(st lane.Stage) {
	if s.stages == "" {
		return
	}
	if err := lane.AppendStage(s.stages, st); err != nil {
		fmt.Fprintf(s.io.Err, "baton: stage %s was not recorded: %v\n", s.name, err)
	}
}

// A stagePreset knows a test runner: the lines of its output that name a failed test, and how to run only
// some tests.
type stagePreset struct {
	failure func(line string) string                // the id of the test a line says failed, or ""
	narrow  func(ids []string) (name, value string) // the variable that runs only ids
}

var stagePresets = map[string]stagePreset{
	"go":     {failure: goFailure, narrow: goNarrow},
	"pytest": {failure: pytestFailure, narrow: pytestNarrow},
}

// goFailure reads go test's `--- FAIL: TestX/sub (0.01s)` as its top-level test, TestX: go test -run splits
// its pattern at slashes, and a top-level test runs its subtests.
func goFailure(line string) string {
	rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "--- FAIL: ")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, " ")
	name, _, _ = strings.Cut(name, "/")
	if !lane.ValidFailID(name) {
		return ""
	}
	return name
}

// goNarrow runs only the top-level tests ids, through GOFLAGS, which go test reads and go build and go vet
// pass over.
func goNarrow(ids []string) (string, string) { return "GOFLAGS", "-run=" + lane.FailedRE(ids) }

// pytestFailure reads pytest's summary line `FAILED tests/test_x.py::test_a[p] - AssertionError` as the
// test's node id, which ends where its message starts: at the first " - " past its parameters, if any.
func pytestFailure(line string) string {
	rest, ok := strings.CutPrefix(line, "FAILED ")
	if !ok {
		return ""
	}
	from := 0
	if open := strings.IndexByte(rest, '['); open >= 0 && !strings.Contains(rest[:open], " - ") {
		if end := strings.IndexByte(rest[open:], ']'); end >= 0 {
			from = open + end
		}
	}
	id := rest
	if i := strings.Index(rest[from:], " - "); i >= 0 {
		id = rest[:from+i]
	}
	if id = strings.TrimSpace(id); !strings.Contains(id, "::") || !lane.ValidFailID(id) {
		return ""
	}
	return id
}

// pytestNarrow runs only the tests ids, by their names (-k matches names, not paths) through
// PYTEST_ADDOPTS: a test of the same name in another file runs too.
func pytestNarrow(ids []string) (string, string) {
	var names []string
	for _, id := range ids {
		name := id[strings.LastIndex(id, "::")+2:]
		if i := strings.IndexByte(name, '['); i > 0 {
			name = name[:i]
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return "PYTEST_ADDOPTS", `-k "` + strings.Join(names, " or ") + `"`
}
