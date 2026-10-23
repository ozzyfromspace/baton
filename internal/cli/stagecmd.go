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
	register("stage", "run one stage of a gate script: stage <name> [--keep-going | --halts] [--runner "+runnerNames("|")+"] [--log <file>] -- <command…>. "+
		"It marks the stage in the gate's log, stops the gate at its first failed stage (in a QA phase, every stage runs, and only one with --halts stops it: a setup or a build the later stages need), "+
		"reads what failed from its output and from --log, the file its command writes its output to, and on a rerun runs only what failed", cmdStage)
}

var stageUsage = "usage: baton stage <name> [--keep-going | --halts] [--runner " + runnerNames("|") + "] [--log <file>] -- <command…>"

// runnerNames are the runners --runner takes, joined by sep.
func runnerNames(sep string) string {
	var names []string
	for _, p := range lane.Presets {
		names = append(names, p.Name)
	}
	return strings.Join(names, sep)
}

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
// In a QA phase's gate ($BATON_QA_PHASE) every stage keeps going, so that one run collects every red,
// unless it says --halts: a stage the later ones need (a setup, a build) stops them when it fails.
//
// A process the command leaves running must write somewhere else than the stage's output: the stage stops
// reading that output stageDrain after its command ends. A command that writes its output to a file of its
// own names it with --log, relative to the stage's working directory, and a failed stage reads what failed
// from that file too.
func cmdStage(args []string, io IO) int {
	p, err := parseArgs(args, []string{"runner", "log"}, []string{"keep-going", "halts"})
	if err != nil {
		fmt.Fprintf(io.Err, "baton: %v\n%s\n", err, stageUsage)
		return 2
	}
	if p.dash != 1 || len(p.pos) < 2 {
		fmt.Fprintln(io.Err, stageUsage)
		return 2
	}
	if p.bools["keep-going"] && p.bools["halts"] {
		fmt.Fprintf(io.Err, "baton: a stage keeps going (--keep-going) or halts the gate when it fails (--halts), not both\n%s\n", stageUsage)
		return 2
	}
	logFile, hasLog := p.vals["log"]
	if hasLog && strings.TrimSpace(logFile) == "" {
		fmt.Fprintf(io.Err, "baton: --log names the file the stage's command writes its output to\n%s\n", stageUsage)
		return 2
	}
	name := p.pos[0]
	if !lane.ValidStageName(name) {
		fmt.Fprintf(io.Err, "baton: a stage's name is 1 to %d characters with no spaces, not %q\n", lane.MaxStageName, name)
		return 2
	}
	// With no --runner, the stage reads every runner's failure lines, as a red gate's log is read
	// (lane.FailureIDs): what failed in it is known, and a rerun narrows by the runner that named it.
	var preset *lane.Preset
	presets := lane.Presets
	if r := p.vals["runner"]; r != "" {
		pr, ok := lane.PresetFor(r)
		if !ok {
			fmt.Fprintf(io.Err, "baton: --runner is one of %s, not %q\n", runnerNames(", "), r)
			return 2
		}
		preset, presets = &pr, []lane.Preset{pr}
	}
	qa := io.Env(lane.EnvQAPhase) == "1"
	s := &stageRun{name: name, argv: p.pos[1:], keepGoing: p.bools["keep-going"] || qa && !p.bools["halts"], qa: qa, preset: preset, io: io,
		stages: io.Env(lane.EnvStages), commit: io.Env("BATON_COMMIT"), reading: lane.NewReading(lane.MaxFailures, presets...),
		logName: logFile, log: stageLog(io, logFile)}
	return s.run()
}

// stageRun is one run of `baton stage`.
type stageRun struct {
	name      string
	argv      []string
	keepGoing bool
	qa        bool // a QA phase's gate ($BATON_QA_PHASE)
	preset    *lane.Preset
	io        IO
	stages    string // the job's stages file ("": outside a job)
	commit    string
	logName   string // --log as given ("": none)
	log       string // the file it names

	mu      sync.Mutex
	open    bool // the output so far ends mid-line
	stopped bool // the stage no longer reads its command's output
	reading *lane.Reading
}

func (s *stageRun) run() int {
	if s.stages != "" {
		done, err := lane.ReadStages(s.stages)
		if err != nil {
			fmt.Fprintf(s.io.Err, "baton: %v\n", err)
		}
		if failed, ok := lane.Halted(done); ok {
			if s.qa {
				s.say(decide.StageHaltedQA(s.name, failed))
			} else {
				s.say(decide.StageHalted(s.name, failed))
			}
			s.say(lane.EndMarker(s.name, lane.StageSkipped, 1, 0))
			s.record(lane.Stage{Name: s.name, Status: lane.StageSkipped, Exit: 1, Commit: s.commit, KeepGoing: s.keepGoing})
			return 1
		}
	}
	prior, from := s.inRedGate()
	if prior != nil && prior.From != "" {
		// The job the stage's record comes from: a carried stage's, or each stage of the record a rerun runs
		// from (state.Store.JobRerun), which is a file of the rerun's own.
		from = prior.From
	}
	var narrow []string
	runner := ""
	switch {
	case prior != nil && (prior.Status == lane.StagePassed || prior.Status == lane.StageCarried):
		s.say(decide.StageCarried(s.name, from, prior.Commit))
		s.say(lane.EndMarker(s.name, lane.StageCarried, 0, 0))
		s.record(lane.Stage{Name: s.name, Status: lane.StageCarried, Commit: prior.Commit, From: from, KeepGoing: s.keepGoing})
		return 0
	case prior != nil && prior.Status == lane.StageFailed:
		narrow, runner = prior.Failures, prior.Runner
	}

	env, cleanup, err := s.env(narrow, runner)
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
		s.readLog()
		s.mu.Lock()
		fs, more := s.reading.Failed()
		s.mu.Unlock()
		if s.preset == nil {
			fs = failLinesFirst(fs)
		}
		st.Failures, st.Runner, st.Unnarrowed = narrowTo(fs, more)
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
// (lane.EnvFailed), their pattern (lane.EnvFailedRE) and the narrowing of the runner that named them in the
// red gate (its variable, Preset.Narrow, and its own arguments, lane.EnvFailedArgs), or else of the stage's
// --runner. cleanup removes the file.
func (s *stageRun) env(ids []string, runner string) (env []string, cleanup func(), err error) {
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
	preset := s.preset
	if p, ok := lane.PresetFor(runner); ok {
		preset = &p
	}
	if preset != nil && preset.Narrow != nil {
		k, v := preset.Narrow(ids)
		if was := s.io.Env(k); was != "" {
			v = was + " " + v
		}
		set = append(set, [2]string{k, v})
	}
	if preset != nil && preset.Args != nil && preset.Name == runner {
		if args := preset.Args(ids); args != "" {
			set = append(set, [2]string{lane.EnvFailedArgs, args})
		}
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
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = s.io.Dir, env, s.io.In, pw, pw
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
	s.reading.Line(string(chunk))
}

// narrowTo is the ids a rerun of a failed stage runs, of the tests its reading kept (fs) with more past
// them, and the runner whose own lines named every one of them, "" for fail lines'. With none, the rerun
// runs the stage whole, and why says why (lane.Stage.Unnarrowed): no test was named, a file or package
// failed whole, which no test's name reruns, there are too many, or two runners named them, and no one
// runner's narrowing runs both's.
func narrowTo(fs []lane.FailedTest, more int) (ids []string, runner, why string) {
	var runners []string
	for i, f := range fs {
		if f.Whole {
			return nil, "", lane.UnnarrowedWhole
		}
		if !slices.Contains(ids, f.ID) {
			ids = append(ids, f.ID)
		}
		if f.Runner != "" && !slices.Contains(runners, f.Runner) {
			runners = append(runners, f.Runner)
		}
		if i == 0 {
			runner = f.Runner
		} else if f.Runner != runner {
			runner = ""
		}
	}
	switch {
	case len(ids) == 0:
		return nil, "", lane.UnnarrowedNoIDs
	case more > 0:
		return nil, "", lane.UnnarrowedMany
	case len(runners) > 1:
		return nil, "", lane.UnnarrowedRunners
	}
	return ids, runner, ""
}

// failLinesFirst is what failed in a stage given no --runner: the ids of its command's ::baton fail lines,
// when it printed any, else its runners' own.
func failLinesFirst(fs []lane.FailedTest) []lane.FailedTest {
	var lines []lane.FailedTest
	for _, f := range fs {
		if f.Runner == "" {
			lines = append(lines, f)
		}
	}
	if len(lines) > 0 {
		return lines
	}
	return fs
}

// readLog reads the file the stage's command wrote its output to (--log) for what failed, as its output is
// read; a file it cannot read is said, and changes nothing else.
func (s *stageRun) readLog() {
	if s.log == "" {
		return
	}
	s.mu.Lock()
	err := lane.ReadInto(s.reading, s.log)
	s.mu.Unlock()
	if err != nil {
		s.say(decide.StageLogUnread(s.name, s.logName, err))
	}
}

// stageLog is the file --log names (name), relative to the directory the stage runs in; "" for none.
func stageLog(io IO, name string) string {
	if name == "" || filepath.IsAbs(name) {
		return name
	}
	if wd, err := io.wd(); err == nil {
		return filepath.Join(wd, name)
	}
	return name
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
