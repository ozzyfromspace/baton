package lane

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// A job's log keeps the start of its output and a rolling tail, so a gate that prints without end never
// fills the disk: once the file reaches MaxBytes, everything after the first HeadBytes is cut down to its
// most recent half, behind a line saying how much was cut.
const (
	HeadBytes = 256 << 10
	MaxBytes  = 16 << 20
)

// Log is a job's capped log file. It is safe for concurrent use.
type Log struct {
	mu        sync.Mutex
	path      string
	f         *os.File
	size      int64 // bytes in the file
	head      int64 // bytes kept from the start, once something has been cut
	mark      int64 // length of the line saying what was cut, after the head; 0 until something is
	cut       int64 // bytes cut so far
	headBytes int64
	maxBytes  int64
}

// CreateLog creates (or empties) the log at path, making its directory.
func CreateLog(path string) (*Log, error) { return createLog(path, HeadBytes, MaxBytes) }

func createLog(path string, head, max int64) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, f: f, headBytes: head, maxBytes: max}, nil
}

// Path is the log's file.
func (l *Log) Path() string { return l.path }

// Write appends p, cutting the middle of the log whenever it would grow past its cap.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if l.size >= l.maxBytes {
			if err := l.trim(); err != nil {
				return written, err
			}
		}
		chunk := p
		if room := l.maxBytes - l.size; int64(len(chunk)) > room {
			chunk = chunk[:room]
		}
		n, err := l.f.Write(chunk)
		l.size += int64(n)
		written += n
		p = p[n:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// trimAlign is how far trim looks for the start of a line to cut at.
const trimAlign = 4 << 10

// trim rewrites the log as its head, a line saying how much is cut, and the most recent half of the rest,
// starting at a line where one starts nearby. The rewrite replaces the file whole, so a reader never sees
// it half done.
func (l *Log) trim() error {
	if l.mark == 0 {
		l.head = min(l.headBytes, l.size)
	}
	start := l.head + l.mark
	from := max(l.size-(l.maxBytes-l.head)/2, start)
	tail := make([]byte, l.size-from)
	if _, err := l.f.ReadAt(tail, from); err != nil && err != io.EOF {
		return err
	}
	if i := bytes.IndexByte(tail[:min(len(tail), trimAlign)], '\n'); i >= 0 {
		tail, from = tail[i+1:], from+int64(i+1)
	}
	head := make([]byte, l.head)
	if _, err := l.f.ReadAt(head, 0); err != nil && err != io.EOF {
		return err
	}
	l.cut += from - start
	mark := fmt.Appendf(nil, "\n[baton] … %d bytes of output cut here …\n", l.cut)

	tmp, err := os.CreateTemp(filepath.Dir(l.path), "."+filepath.Base(l.path)+".tmp-*")
	if err != nil {
		return err
	}
	for _, b := range [][]byte{head, mark, tail} {
		if _, err := tmp.Write(b); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	l.f.Close() // Windows renames nothing over an open file
	renamed := os.Rename(tmp.Name(), l.path)
	if renamed != nil {
		os.Remove(tmp.Name())
	}
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	if renamed != nil {
		l.cut -= from - start
		return renamed
	}
	l.mark, l.size = int64(len(mark)), l.head+int64(len(mark))+int64(len(tail))
	return nil
}

// Close closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Tail returns the last lines of the log at path: at most lines of them, and at most maxBytes in all. A
// line cut by the byte limit is left out, unless it is the only one there is.
func Tail(path string, lines, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tail, _, err := tailOf(f, lines, maxBytes)
	return tail, err
}

// tailOf is the last lines of f (Tail), and where they start in it.
func tailOf(f *os.File, lines, maxBytes int) (string, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	from := max(fi.Size()-int64(maxBytes), 0)
	buf := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
		return "", 0, err
	}
	whole := strings.TrimRight(string(buf), "\n")
	text := whole
	if from > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	all := strings.Split(text, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	tail := strings.Join(all, "\n")
	return tail, from + int64(len(whole)-len(tail)), nil // the tail ends where whole does
}

// An Excerpt is what a job's result shows of its log: its last lines, and the lines before those that
// report a failure, so that a failure early in a long log is never lost behind its end.
type Excerpt struct {
	Tail     string
	Failures []string // in order, at most the max asked for, each clipped
	More     int      // the lines that report a failure past Failures
}

// ReadExcerpt reads the Excerpt of the log at path: its last lines (Tail, with lines and maxBytes), and at
// most fails of the lines before them that report a failure (Failure), each clipped to clip bytes.
func ReadExcerpt(path string, lines, maxBytes, fails, clip int) (Excerpt, error) {
	f, err := os.Open(path)
	if err != nil {
		return Excerpt{}, err
	}
	defer f.Close()
	var e Excerpt
	var start int64
	if e.Tail, start, err = tailOf(f, lines, maxBytes); err != nil {
		return e, err
	}
	eachLine(io.NewSectionReader(f, 0, start), func(text string) {
		if !Failure(text) {
			return
		}
		if len(e.Failures) < fails {
			e.Failures = append(e.Failures, clipLine(strings.TrimRight(text, " \t\r"), clip))
		} else {
			e.More++
		}
	})
	return e, nil
}

// Failure reports whether line, from a command's output, reports a failure in a way common test runners
// and build tools do: Go's `--- FAIL`, `FAIL<tab>package` and `panic:`, the word FAILED (pytest, cargo
// and others), or a cross (✗, ✘).
func Failure(line string) bool {
	t := strings.TrimLeft(line, " \t")
	switch {
	case strings.HasPrefix(t, "--- FAIL"), strings.HasPrefix(t, "panic: "),
		strings.HasPrefix(line, "FAIL\t"), strings.HasPrefix(line, "FAIL "), strings.TrimRight(line, " \r") == "FAIL",
		strings.ContainsAny(line, "✗✘"):
		return true
	}
	for i := 0; ; {
		k := strings.Index(line[i:], "FAILED")
		if k < 0 {
			return false
		}
		at, end := i+k, i+k+len("FAILED")
		if (at == 0 || !letter(line[at-1])) && (end == len(line) || !letter(line[end])) {
			return true
		}
		i = end
	}
}

func letter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' }

// clipLine cuts line to at most n bytes, at a character's start, marking the cut with an ellipsis.
func clipLine(line string, n int) string {
	if len(line) <= n {
		return line
	}
	for n > 0 && !utf8.RuneStart(line[n]) {
		n--
	}
	return line[:n] + "…"
}
