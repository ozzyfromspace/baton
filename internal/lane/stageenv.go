package lane

import (
	"io"
	"os"
	"slices"
	"strings"
)

// Environment calls a log the environment's only when no test failed anywhere in it, so a staged gate whose
// build the machine killed, beside a test stage that failed on its own, was read as the change's (a real
// run's QA set, three whole runs). StageEnvironment reads each failed stage's own part of the log instead:
// a test failure in another stage no longer hides it.

// StageEnvironment reads the log at path, of a gate whose stages are as recorded (ReadStages, Job.Stages),
// for the stages its environment took: each stage that failed with no failure ids whose part of the log,
// from its start marker to its end marker, shows an environment failure (envSigns), by the first line that
// does, keyed by the stage's name. A stage that failed tests by name is passed over: those recorded as its
// failures, more than a stage records (UnnarrowedMany), or from more than one runner (UnnarrowedRunners).
// A runner's line for a file or package that failed with no test named does not hide it, since a process
// killed for memory leaves just that. A line in a stage nested in another is the inner one's. nil for none,
// or a log that cannot be read.
func StageEnvironment(path string, stages []Stage) map[string]EnvMatch {
	want := map[string]bool{}
	for _, s := range stages {
		if s.Status == StageFailed && len(s.Failures) == 0 && s.Unnarrowed != UnnarrowedMany && s.Unnarrowed != UnnarrowedRunners {
			want[s.Name] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return stageEnvOf(f, want)
}

// stageEnvOf is StageEnvironment of the log r, for the stages in want. The lines baton's runner writes
// into it ("[baton] job j1: …") are passed over, as by Environment.
func stageEnvOf(r io.Reader, want map[string]bool) map[string]EnvMatch {
	var open []string // the stages started and not yet ended, the innermost last
	found := map[string]EnvMatch{}
	eachLine(r, func(line string) {
		if m, ok := ParseMarker(line); ok {
			switch m.Kind {
			case MarkStart:
				open = append(slices.DeleteFunc(open, func(n string) bool { return n == m.Stage }), m.Stage)
			case MarkEnd:
				open = slices.DeleteFunc(open, func(n string) bool { return n == m.Stage })
			}
			return
		}
		if len(open) == 0 || strings.HasPrefix(line, "[baton") {
			return
		}
		in := open[len(open)-1]
		if !want[in] || found[in].Kind != "" {
			return
		}
		if m := EnvironmentLine(plainLine(line)); m.Kind != "" {
			found[in] = m
		}
	})
	if len(found) == 0 {
		return nil
	}
	return found
}
