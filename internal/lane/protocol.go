package lane

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The gate protocol: how a gate's command tells baton what it is doing while it runs, so that a failure is
// known the moment it is printed, and a rerun runs only what failed.
//
// Markers. A gate prints each on a line of its own, into its output (the job's log); leading spaces and a
// trailing carriage return are allowed, anything else on the line is not:
//
//	::baton stage <name> start
//	::baton stage <name> <passed|failed|skipped|carried> exit=<n> seconds=<n>
//	::baton fail <id>
//
// A stage is a named step of the gate (lint, unit, e2e); its name has no spaces (ValidStageName). A fail
// line names one thing that failed, a test's id as its runner names it, so that a rerun can run exactly
// that (ValidFailID). `baton stage <name> -- <command>` prints a stage's markers around its command and
// records it; a script may print them itself. ParseMarker reads a line.
//
// The stages file. Each ended stage is a JSON line (Stage) in jobs/<id>.stages, beside the job's log,
// appended as it ends (AppendStage) by `baton stage` or the runner, and read back by ReadStages. A gate
// stops at its first failed stage, across the separate commands of a script, by reading it (Halted).
//
// The environment. A gate's command gets, besides the lane's (Pool.env, which strips every BATON_ variable
// it inherits), EnvJob (the job's id), EnvStages (this job's stages file) and, on a rerun of a red gate,
// EnvRerun (the record of the red gate it reruns: a stages file, in which a stage names the job its record
// comes from, Stage.From, when that is not the file's own). A stage narrowed to what failed in the red
// gate also gets EnvFailed (a file of the ids, one a line) and EnvFailedRE (FailedRE of them).

// Environment variables of the gate protocol.
const (
	EnvJob      = "BATON_JOB"
	EnvStages   = "BATON_STAGES"
	EnvRerun    = "BATON_RERUN"
	EnvFailed   = "BATON_FAILED"
	EnvFailedRE = "BATON_FAILED_RE"
)

// How a stage ended. A carried stage is one a rerun did not run again, as it passed in the red gate the
// rerun stands in for (Stage.From).
const (
	StagePassed  = "passed"
	StageFailed  = "failed"
	StageSkipped = "skipped"
	StageCarried = "carried"
)

// Marker kinds.
const (
	MarkStart = "start"
	MarkEnd   = "end"
	MarkFail  = "fail"
)

// Limits of the protocol: a stage's name, a fail line's id, and the ids a stage records.
const (
	MaxStageName = 64
	MaxFailID    = 512
	MaxFailures  = 100
)

// Marker is one marker line of a gate's output.
type Marker struct {
	Kind    string  // MarkStart, MarkEnd or MarkFail
	Stage   string  // a stage marker's stage
	Status  string  // an end marker's: StagePassed, StageFailed, StageSkipped or StageCarried
	Exit    int     // an end marker's exit code
	Seconds float64 // an end marker's: how long the stage ran
	ID      string  // a fail marker's: what failed
}

const markerPrefix = "::baton "

// ParseMarker reads line as a marker of the gate protocol, reporting whether it is one.
func ParseMarker(line string) (Marker, bool) {
	t := strings.TrimRight(strings.TrimLeft(line, " \t"), "\r\n")
	rest, ok := strings.CutPrefix(t, markerPrefix)
	if !ok {
		return Marker{}, false
	}
	if id, ok := strings.CutPrefix(rest, "fail "); ok {
		id = strings.TrimSpace(id)
		if !ValidFailID(id) {
			return Marker{}, false
		}
		return Marker{Kind: MarkFail, ID: id}, true
	}
	f := strings.Fields(rest)
	if len(f) < 3 || f[0] != "stage" || !ValidStageName(f[1]) {
		return Marker{}, false
	}
	m := Marker{Stage: f[1]}
	switch {
	case len(f) == 3 && f[2] == "start":
		m.Kind = MarkStart
		return m, true
	case len(f) == 5 && stageStatus(f[2]):
		exit, err := strconv.Atoi(strings.TrimPrefix(f[3], "exit="))
		if err != nil || !strings.HasPrefix(f[3], "exit=") {
			return Marker{}, false
		}
		secs, err := strconv.ParseFloat(strings.TrimPrefix(f[4], "seconds="), 64)
		if err != nil || !strings.HasPrefix(f[4], "seconds=") || secs < 0 || math.IsInf(secs, 0) || math.IsNaN(secs) {
			return Marker{}, false
		}
		m.Kind, m.Status, m.Exit, m.Seconds = MarkEnd, f[2], exit, secs
		return m, true
	}
	return Marker{}, false
}

func stageStatus(s string) bool {
	return s == StagePassed || s == StageFailed || s == StageSkipped || s == StageCarried
}

// ValidStageName reports whether name can name a stage: 1 to MaxStageName bytes of UTF-8, with no space or
// control character.
func ValidStageName(name string) bool {
	return name != "" && len(name) <= MaxStageName && utf8.ValidString(name) &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// ValidFailID reports whether id can be a fail line's: 1 to MaxFailID bytes of UTF-8 with no control
// character, and no space at either end. A test's id may have spaces inside (a parametrized pytest id).
func ValidFailID(id string) bool {
	return id != "" && len(id) <= MaxFailID && utf8.ValidString(id) && strings.TrimSpace(id) == id &&
		strings.IndexFunc(id, unicode.IsControl) < 0
}

// StartMarker is the line that says stage name starts.
func StartMarker(name string) string { return markerPrefix + "stage " + name + " start" }

// EndMarker is the line that says how stage name ended, after how long (in whole seconds).
func EndMarker(name, status string, exit int, took time.Duration) string {
	return markerPrefix + "stage " + name + " " + status + " exit=" + strconv.Itoa(exit) +
		" seconds=" + strconv.FormatInt(int64(took.Round(time.Second)/time.Second), 10)
}

// FailMarker is the line that says id failed.
func FailMarker(id string) string { return markerPrefix + "fail " + id }

// Stage is one ended stage of a gate, a line of its stages file.
type Stage struct {
	Name    string    `json:"name"`
	Status  string    `json:"status"` // StagePassed, StageFailed, StageSkipped or StageCarried
	Exit    int       `json:"exit"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	// Commit is the commit the stage ran on (BATON_COMMIT); a carried stage keeps the one it passed on.
	Commit string `json:"commit,omitempty"`
	// Failures are the ids of what failed in the stage (its fail lines, or its runner's own lines), at most
	// MaxFailures: what a rerun of it narrows to.
	Failures []string `json:"failures,omitempty"`
	// KeepGoing: the stage was run with --keep-going, so its failure does not stop the stages after it.
	KeepGoing bool `json:"keep_going,omitempty"`
	// From is a carried stage's: the red gate's job it passed in.
	From string `json:"from,omitempty"`
}

// Took is how long the stage ran.
func (s Stage) Took() time.Duration {
	if s.Started.IsZero() || s.Ended.Before(s.Started) {
		return 0
	}
	return s.Ended.Sub(s.Started)
}

// Halted returns the stage that stops the gate's later stages, the first that failed without
// --keep-going, reporting whether there is one.
func Halted(stages []Stage) (Stage, bool) {
	for _, s := range stages {
		if s.Status == StageFailed && !s.KeepGoing {
			return s, true
		}
	}
	return Stage{}, false
}

// AppendStage adds s to the stages file at path as one JSON line, written in one call so that the lines of
// two writers never interleave.
func AppendStage(path string, s Stage) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ReadStages reads the stages file at path, oldest first; none when there is no file. A line that is not a
// stage (one a writer was stopped in the middle of) is passed over.
func ReadStages(path string) ([]Stage, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var stages []Stage
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var s Stage
			if json.Unmarshal(line, &s) == nil && s.Name != "" {
				stages = append(stages, s)
			}
		}
		if errors.Is(err, io.EOF) {
			return stages, nil
		}
		if err != nil {
			return stages, err
		}
	}
}

// FailedRE is an anchored alternation of ids, each matched literally: ^(a|b)$.
func FailedRE(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = regexp.QuoteMeta(id)
	}
	return "^(" + strings.Join(q, "|") + ")$"
}
