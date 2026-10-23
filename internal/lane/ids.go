package lane

import (
	"bufio"
	"io"
	"slices"
	"strings"
)

// FailedTest is one test a gate's output says failed.
type FailedTest struct {
	// Runner is the Preset that read it, or "" for a ::baton fail line.
	Runner string `json:"runner,omitempty"`
	// ID is the test as its runner names it, or what a fail line named; for a file or package that failed
	// whole (Whole), that file or package.
	ID string `json:"id"`
	// Scope is where the test ran when its id does not say: a go test's package, a Playwright test's project.
	Scope string `json:"scope,omitempty"`
	// Whole: ID is a file or package that failed with no test of it named (it did not build or load, or a
	// test timed out), so only a run of all of it reruns what failed.
	Whole bool `json:"whole,omitempty"`
}

// A Reading collects the tests a gate's output says failed, a line at a time: its ::baton fail lines, and
// the failure lines of the runners it reads. Each test is kept once, in the order it was first named, up to
// its max; the rest are counted.
type Reading struct {
	max     int
	readers []reader
	at      map[FailedTest]int // each test, and when it was first named
	seq     int
}

// NewReading reads ::baton fail lines and the output of the runners presets, keeping at most max tests.
func NewReading(max int, presets ...Preset) *Reading {
	r := &Reading{max: max, at: map[FailedTest]int{}}
	for _, p := range presets {
		r.readers = append(r.readers, p.read(r))
	}
	return r
}

// Line reads one whole line of output.
func (r *Reading) Line(line string) {
	if m, ok := ParseMarker(line); ok {
		if m.Kind == MarkFail {
			r.add(FailedTest{ID: m.ID})
		}
		return
	}
	line = plainLine(line)
	for _, rd := range r.readers {
		rd.line(line)
	}
}

// Failed ends the reading: the tests read, in the order first named, at most its max, and how many more
// there are.
func (r *Reading) Failed() ([]FailedTest, int) {
	for _, rd := range r.readers {
		if rd.flush != nil {
			rd.flush()
		}
	}
	all := make([]FailedTest, 0, len(r.at))
	for f := range r.at {
		all = append(all, f)
	}
	slices.SortFunc(all, func(a, b FailedTest) int { return r.at[a] - r.at[b] })
	if len(all) > r.max {
		return all[:r.max], len(all) - r.max
	}
	return all, 0
}

// next is a place in the order of the tests read, for a reader that names a test only once more lines are
// read (addAt).
func (r *Reading) next() int {
	r.seq++
	return r.seq
}

func (r *Reading) add(f FailedTest) { r.addAt(r.next(), f) }

func (r *Reading) addAt(seq int, f FailedTest) {
	if _, ok := r.at[f]; !ok {
		r.at[f] = seq
	}
}

// FailureIDs reads the log at path for the tests it says failed (a Reading of every Preset): at most max of
// them (MaxFailures when max is 0 or more than that), and how many more there are.
func FailureIDs(path string, max int) ([]FailedTest, int, error) {
	if max <= 0 || max > MaxFailures {
		max = MaxFailures
	}
	r := NewReading(max, Presets...)
	if err := ReadInto(r, path); err != nil {
		return nil, 0, err
	}
	fs, more := r.Failed()
	return fs, more, nil
}

// Reruns are the commands that rerun only the tests fs, one for each runner, in the order its first test
// comes; an id a fail line named has none.
func Reruns(fs []FailedTest) []string {
	var runners []string
	for _, f := range fs {
		if f.Runner != "" {
			runners = appendNew(runners, f.Runner)
		}
	}
	var cmds []string
	for _, name := range runners {
		p, _ := PresetFor(name)
		var its []FailedTest
		for _, f := range fs {
			if f.Runner == name {
				its = append(its, f)
			}
		}
		if cmd := p.Rerun(its); cmd != "" {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// eachLine calls fn with each line r reads, without its line end; of a line longer than the reader's buffer,
// with its start alone.
func eachLine(r io.Reader, fn func(line string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, long, err := br.ReadLine()
		if len(line) > 0 || err == nil {
			fn(string(line))
		}
		for long && err == nil {
			_, long, err = br.ReadLine()
		}
		if err != nil {
			return
		}
	}
}

// plainLine is line without its line end and its terminal escapes (colour, cursor moves, links).
func plainLine(line string) string {
	line = strings.TrimRight(line, "\r\n")
	if strings.IndexByte(line, 0x1b) < 0 {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			b.WriteByte(line[i])
			i++
			continue
		}
		i++
		switch {
		case i >= len(line):
		case line[i] == '[': // CSI: parameters, then a final byte in @–~
			for i++; i < len(line) && (line[i] < 0x40 || line[i] > 0x7e); i++ {
			}
			i++
		case line[i] == ']': // OSC, to BEL or ST
			for i++; i < len(line); i++ {
				if line[i] == 0x07 {
					i++
					break
				}
				if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '\\' {
					i += 2
					break
				}
			}
		default: // intermediates, then a final byte: ESC ( B
			for ; i < len(line) && line[i] >= 0x20 && line[i] <= 0x2f; i++ {
			}
			i++
		}
	}
	return b.String()
}
