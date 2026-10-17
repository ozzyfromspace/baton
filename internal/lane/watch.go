package lane

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

// What a Watcher reads of a line, and quotes of one that shows a failure.
const (
	watchLine   = 64 << 10 // the most of a line a Watcher reads: a failure or a marker shows in its start
	FailingClip = 300      // the most of a failure's line a Watcher quotes (Watcher.Failed)

	maxStageSeconds = 1e9 // more than any stage runs, and less than a time.Duration holds
)

// A Watcher reads a gate's output as it is written, a line at a time, for what the gate protocol's markers
// and the lines that report a failure say (ParseMarker, Failure), and tells of it as it happens: each
// stage as its end marker comes, and the gate's first failure, once. It passes every byte on to Out
// unchanged before it reads it, and is safe for concurrent use. Flush reads a last line left unended.
//
// A stage it tells of is what its markers say: it started when its start marker came (or its seconds
// before its end, with none), and its Failures are the fail lines between the two, for a failed stage
// with no more than MaxFailures of them. A stage `baton stage` ran also has a record of its own in the
// stages file, which knows more: KeepGoing, and the failure ids its runner's preset reads.
type Watcher struct {
	Out    io.Writer
	Now    func() time.Time // nil: time.Now
	Commit string           // the commit the gate runs on, for the stages it tells of

	// Ended is told of each stage as its end marker comes.
	Ended func(Stage)
	// Failed is told of the gate's first failure, once: the line that shows it (a line that reports a
	// failure, a fail marker, or a failed stage's end marker), trimmed and clipped to FailingClip, and the
	// stage it came in ("" for none).
	//
	// Both are called from Write, holding the Watcher's lock: they must not write to it.
	Failed func(line, stage string)

	mu      sync.Mutex
	partial []byte    // the line being read, up to watchLine bytes of it
	stage   string    // the stage started and not yet ended, "" for none
	since   time.Time // when it started
	ids     []string  // its fail lines' ids
	over    bool      // it had more than MaxFailures
	failed  bool      // Failed was told
}

// Write passes p on to Out, and reads the lines it ends.
func (w *Watcher) Write(p []byte) (int, error) {
	n, err := w.Out.Write(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	for p = p[:n]; len(p) > 0; {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.keep(p)
			break
		}
		if len(w.partial) == 0 {
			w.line(string(p[:min(i, watchLine)]))
		} else {
			w.keep(p[:i])
			w.line(string(w.partial))
			w.partial = w.partial[:0]
		}
		p = p[i+1:]
	}
	return n, err
}

// Flush reads the line the output ended in the middle of, if any.
func (w *Watcher) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.line(string(w.partial))
		w.partial = w.partial[:0]
	}
}

// keep adds b to the line being read, keeping no more than watchLine bytes of it.
func (w *Watcher) keep(b []byte) {
	if room := watchLine - len(w.partial); len(b) > room {
		b = b[:max(room, 0)]
	}
	w.partial = append(w.partial, b...)
}

// line reads one line of the output.
func (w *Watcher) line(text string) {
	m, ok := ParseMarker(text)
	if !ok {
		if Failure(text) {
			w.fail(text, w.stage)
		}
		return
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	switch m.Kind {
	case MarkStart:
		w.stage, w.since, w.ids, w.over = m.Stage, now(), nil, false
	case MarkFail:
		switch {
		case w.stage == "" || slices.Contains(w.ids, m.ID):
		case len(w.ids) < MaxFailures:
			w.ids = append(w.ids, m.ID)
		default:
			w.over = true
		}
		w.fail(text, w.stage)
	case MarkEnd:
		at := now()
		took := time.Duration(min(m.Seconds, maxStageSeconds) * float64(time.Second))
		st := Stage{Name: m.Stage, Status: m.Status, Exit: m.Exit, Started: at.Add(-took), Ended: at, Commit: w.Commit}
		if m.Stage == w.stage {
			st.Started = w.since
			if m.Status == StageFailed && !w.over {
				st.Failures = w.ids
			}
			w.stage, w.ids, w.over = "", nil, false
		}
		if w.Ended != nil {
			w.Ended(st)
		}
		if m.Status == StageFailed {
			w.fail(text, m.Stage)
		}
	}
}

// fail tells Failed of the gate's first failure.
func (w *Watcher) fail(line, stage string) {
	if w.failed {
		return
	}
	w.failed = true
	if w.Failed != nil {
		w.Failed(clipLine(strings.TrimSpace(line), FailingClip), stage)
	}
}
